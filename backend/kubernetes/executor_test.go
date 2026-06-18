package kubernetes_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	pushdown "cel-pushdown"
	kubepushdown "cel-pushdown/backend/kubernetes"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestExecuteExactGet(t *testing.T) {
	client := fakeClient(pod("default", "nginx", nil))
	plan := mustPlan(t, `
		object.apiVersion == "v1" &&
		object.kind == "Pod" &&
		object.metadata.namespace == "default" &&
		object.metadata.name == "nginx"
	`)
	got := executeAndFilter(t, client, plan)
	if len(got) != 1 || got[0].GetName() != "nginx" {
		t.Fatalf("results = %v, want nginx", objectNames(got))
	}
}

func TestExecuteLabelSelector(t *testing.T) {
	client := fakeClient(
		pod("default", "web-1", map[string]string{"app": "web"}),
		pod("default", "api-1", map[string]string{"app": "api"}),
		pod("default", "web-2", map[string]string{"app": "web"}),
	)
	plan := mustPlan(t, `
		object.apiVersion == "v1" &&
		object.kind == "Pod" &&
		object.metadata.labels["app"] == "web"
	`)
	got := executeAndFilter(t, client, plan)
	names := objectNames(got)
	if join(names) != "web-1,web-2" {
		t.Fatalf("results = %v, want web-1,web-2", names)
	}
}

func TestExecuteLabelInequalityDoesNotMatchMissingLabel(t *testing.T) {
	client := fakeClient(
		pod("default", "api-1", map[string]string{"app": "api"}),
		pod("default", "web-1", map[string]string{"app": "web"}),
		pod("default", "missing-1", nil),
	)
	plan := mustPlan(t, `
		object.apiVersion == "v1" &&
		object.kind == "Pod" &&
		object.metadata.labels["app"] != "web"
	`)
	got := executeAndFilter(t, client, plan)
	names := objectNames(got)
	if join(names) != "api-1" {
		t.Fatalf("results = %v, want api-1", names)
	}
}

func TestExecuteAndFilterResidual(t *testing.T) {
	client := fakeClient(
		pod("default", "web-1", map[string]string{"app": "web"}),
		pod("default", "api-1", map[string]string{"app": "web"}),
		pod("default", "db-1", map[string]string{"app": "db"}),
	)
	plan := mustPlan(t, `
		object.apiVersion == "v1" &&
		object.kind == "Pod" &&
		object.metadata.namespace == "default" &&
		object.metadata.labels["app"] == "web" &&
		object.metadata.name.startsWith("web-")
	`)
	got := executeAndFilter(t, client, plan)
	names := objectNames(got)
	if join(names) != "web-1" {
		t.Fatalf("results = %v, want web-1", names)
	}
}

func TestExecuteMultipleSearches(t *testing.T) {
	client := fakeClient(
		pod("default", "web", map[string]string{"app": "web"}),
		deployment("default", "web", map[string]string{"app": "web"}, 4),
	)
	plan := mustPlan(t, `
		object.metadata.labels["app"] == "web" &&
		(
			(object.apiVersion == "v1" && object.kind == "Pod") ||
			(object.apiVersion == "apps/v1" && object.kind == "Deployment")
		)
	`)
	got := executeAndFilter(t, client, plan)
	kinds := objectKinds(got)
	if join(kinds) != "Deployment,Pod" {
		t.Fatalf("kinds = %v, want Deployment,Pod", kinds)
	}
}

func TestExecuteDeduplication(t *testing.T) {
	client := fakeClient(pod("default", "web", map[string]string{"app": "web"}))
	plan := mustPlan(t, `
		(
			object.apiVersion == "v1" &&
			object.kind == "Pod" &&
			object.metadata.labels["app"] == "web"
		) ||
		(
			object.apiVersion == "v1" &&
			object.kind == "Pod" &&
			object.metadata.labels["app"] == "web" &&
			object.metadata.name == "web"
		)
	`)
	got := executeAndFilter(t, client, plan)
	names := objectNames(got)
	if join(names) != "web" {
		t.Fatalf("results = %v, want one web", names)
	}
}

func executeAndFilter(t *testing.T, client *dynamicfake.FakeDynamicClient, plan *pushdown.Plan[kubepushdown.Query]) []unstructured.Unstructured {
	t.Helper()
	executor := &kubepushdown.KubernetesExecutor{
		Client:             client,
		Mapper:             testMapper(),
		AllowAllNamespaces: true,
	}
	got, err := executor.ExecuteAndFilter(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteAndFilter() error = %v", err)
	}
	return got
}

func fakeClient(objects ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	runtimeObjects := make([]runtime.Object, 0, len(objects))
	for _, obj := range objects {
		runtimeObjects = append(runtimeObjects, obj)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			podGVR:        "PodList",
			deploymentGVR: "DeploymentList",
			serviceGVR:    "ServiceList",
		},
		runtimeObjects...,
	)
}

func pod(namespace, name string, labels map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{},
		"status": map[string]any{
			"phase": "Running",
		},
	}}
	obj.SetAPIVersion("v1")
	obj.SetKind("Pod")
	obj.SetNamespace(namespace)
	obj.SetName(name)
	obj.SetLabels(labels)
	return obj
}

func deployment(namespace, name string, labels map[string]string, replicas int64) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"replicas": replicas,
		},
	}}
	obj.SetAPIVersion("apps/v1")
	obj.SetKind("Deployment")
	obj.SetNamespace(namespace)
	obj.SetName(name)
	obj.SetLabels(labels)
	return obj
}

func objectNames(objects []unstructured.Unstructured) []string {
	out := make([]string, 0, len(objects))
	for _, obj := range objects {
		out = append(out, obj.GetName())
	}
	sort.Strings(out)
	return out
}

func objectKinds(objects []unstructured.Unstructured) []string {
	out := make([]string, 0, len(objects))
	for _, obj := range objects {
		out = append(out, obj.GetKind())
	}
	sort.Strings(out)
	return out
}

func join(vals []string) string {
	sort.Strings(vals)
	return strings.Join(vals, ",")
}
