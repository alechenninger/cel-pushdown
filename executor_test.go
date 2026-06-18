package celpushdown

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestExecuteAndFilter(t *testing.T) {
	t.Parallel()

	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	gvk := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "PodList"},
		newPod("default", "web-1", "web"),
		newPod("default", "web-2", "api"),
		newPod("default", "api-1", "web"),
		newPod("other", "web-3", "web"),
	)

	planner, err := NewPlanner(PlanOptions{
		AllowAllNamespaces: true,
		FieldSupport:       BuiltinFieldSupport{},
		ResourceResolver:   staticResolver{mapping: map[schema.GroupVersionKind]schema.GroupVersionResource{gvk: gvr}},
	})
	if err != nil {
		t.Fatalf("NewPlanner() error = %v", err)
	}

	plan, err := planner.Plan(context.Background(), `object.apiVersion == "v1" &&
object.kind == "Pod" &&
object.metadata.namespace == "default" &&
object.metadata.labels["app"] == "web" &&
object.metadata.name.startsWith("web-")`)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	items, err := planner.ExecuteAndFilter(context.Background(), client, plan)
	if err != nil {
		t.Fatalf("ExecuteAndFilter() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("ExecuteAndFilter() returned %d items, want 1", len(items))
	}
	if items[0].GetName() != "web-1" {
		t.Fatalf("ExecuteAndFilter() returned %q, want web-1", items[0].GetName())
	}
}

func newPod(namespace, name, app string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]any{
					"app": app,
				},
			},
		},
	}
}
