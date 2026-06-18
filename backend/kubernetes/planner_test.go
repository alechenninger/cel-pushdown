package kubernetes_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	pushdown "cel-pushdown"
	kubepushdown "cel-pushdown/backend/kubernetes"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	podGVK        = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
	podGVR        = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	deploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	deploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	serviceGVK    = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Service"}
	serviceGVR    = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
)

func testMapper() meta.RESTMapper {
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
		{Group: "apps", Version: "v1"},
	})
	mapper.AddSpecific(podGVK, podGVR, schema.GroupVersionResource{Version: "v1", Resource: "pod"}, meta.RESTScopeNamespace)
	mapper.AddSpecific(deploymentGVK, deploymentGVR, schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployment"}, meta.RESTScopeNamespace)
	mapper.AddSpecific(serviceGVK, serviceGVR, schema.GroupVersionResource{Version: "v1", Resource: "service"}, meta.RESTScopeNamespace)
	return mapper
}

func mustPlan(t *testing.T, expr string, opts ...pushdown.PlanOption) *pushdown.Plan[kubepushdown.Query] {
	t.Helper()
	planner := kubepushdown.NewPlanner(kubepushdown.WithRESTMapper(testMapper()))
	plan, err := planner.Plan(context.Background(), expr, opts...)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	return plan
}

func TestPlanSingleExactPodByName(t *testing.T) {
	plan := mustPlan(t, `
		object.apiVersion == "v1" &&
		object.kind == "Pod" &&
		object.metadata.namespace == "default" &&
		object.metadata.name == "nginx"
	`)
	if len(plan.Searches) != 1 {
		t.Fatalf("searches = %d, want 1", len(plan.Searches))
	}
	search := plan.Searches[0]
	if !search.Exact || search.Residual != nil || !plan.Exact {
		t.Fatalf("search exact=%v residual=%v plan exact=%v, want exact with no residual", search.Exact, search.Residual, plan.Exact)
	}
	q := search.Native
	if q.GVK != podGVK || q.GVR != podGVR || !q.ResourceKnown {
		t.Fatalf("query resource = %s/%s known=%v, want pod GVK/GVR known", q.GVK, q.GVR, q.ResourceKnown)
	}
	if q.Namespace != "default" || q.Name != "nginx" {
		t.Fatalf("namespace/name = %q/%q, want default/nginx", q.Namespace, q.Name)
	}
}

func TestPlanLabelEquality(t *testing.T) {
	plan := mustPlan(t, `
		object.apiVersion == "v1" &&
		object.kind == "Pod" &&
		object.metadata.labels["app"] == "web"
	`)
	search := onlySearch(t, plan)
	if !search.Native.LabelSelector.Matches(labels.Set{"app": "web"}) {
		t.Fatalf("selector %q should match app=web", search.Native.RawLabelSelector)
	}
	if search.Native.LabelSelector.Matches(labels.Set{"app": "api"}) {
		t.Fatalf("selector %q should not match app=api", search.Native.RawLabelSelector)
	}
	if search.Residual != nil {
		t.Fatalf("residual = %q, want nil", search.Residual.Source)
	}
}

func TestPlanLabelSetMembership(t *testing.T) {
	plan := mustPlan(t, `object.metadata.labels["app"] in ["web", "api"]`)
	search := onlySearch(t, plan)
	for _, app := range []string{"web", "api"} {
		if !search.Native.LabelSelector.Matches(labels.Set{"app": app}) {
			t.Fatalf("selector %q should match app=%s", search.Native.RawLabelSelector, app)
		}
	}
	if search.Native.LabelSelector.Matches(labels.Set{"app": "db"}) {
		t.Fatalf("selector %q should not match app=db", search.Native.RawLabelSelector)
	}
	if search.Residual != nil {
		t.Fatalf("residual = %q, want nil", search.Residual.Source)
	}
}

func TestPlanLabelInequalityRequiresExistingLabel(t *testing.T) {
	search := onlySearch(t, mustPlan(t, `object.metadata.labels["app"] != "web"`))
	if !search.Native.LabelSelector.Matches(labels.Set{"app": "api"}) {
		t.Fatalf("selector %q should match app=api", search.Native.RawLabelSelector)
	}
	if search.Native.LabelSelector.Matches(labels.Set{"app": "web"}) {
		t.Fatalf("selector %q should not match app=web", search.Native.RawLabelSelector)
	}
	if search.Native.LabelSelector.Matches(labels.Set{"env": "prod"}) {
		t.Fatalf("selector %q should not match missing app", search.Native.RawLabelSelector)
	}
	if search.Residual != nil {
		t.Fatalf("residual = %q, want nil", search.Residual.Source)
	}
}

func TestPlanLabelExistenceAndNonExistence(t *testing.T) {
	exists := onlySearch(t, mustPlan(t, `"app" in object.metadata.labels`))
	if !exists.Native.LabelSelector.Matches(labels.Set{"app": "web"}) {
		t.Fatalf("exists selector %q should match app label", exists.Native.RawLabelSelector)
	}
	if exists.Native.LabelSelector.Matches(labels.Set{"env": "prod"}) {
		t.Fatalf("exists selector %q should not match missing app", exists.Native.RawLabelSelector)
	}

	missing := onlySearch(t, mustPlan(t, `!("app" in object.metadata.labels)`))
	if !missing.Native.LabelSelector.Matches(labels.Set{"env": "prod"}) {
		t.Fatalf("missing selector %q should match missing app", missing.Native.RawLabelSelector)
	}
	if missing.Native.LabelSelector.Matches(labels.Set{"app": "web"}) {
		t.Fatalf("missing selector %q should not match present app", missing.Native.RawLabelSelector)
	}
}

func TestPlanPodFieldSelector(t *testing.T) {
	plan := mustPlan(t, `
		object.apiVersion == "v1" &&
		object.kind == "Pod" &&
		object.status.phase == "Running"
	`)
	search := onlySearch(t, plan)
	if got := search.Native.RawFieldSelector; got != "status.phase=Running" {
		t.Fatalf("field selector = %q, want status.phase=Running", got)
	}
	if search.Residual != nil {
		t.Fatalf("residual = %q, want nil", search.Residual.Source)
	}
}

func TestPlanUnsupportedFieldSelectorResidual(t *testing.T) {
	plan := mustPlan(t, `
		object.apiVersion == "apps/v1" &&
		object.kind == "Deployment" &&
		object.spec.replicas == 3
	`)
	search := onlySearch(t, plan)
	if search.Native.RawFieldSelector != "" {
		t.Fatalf("field selector = %q, want empty", search.Native.RawFieldSelector)
	}
	if search.Residual == nil || !strings.Contains(search.Residual.Source, "object.spec.replicas == 3") {
		t.Fatalf("residual = %#v, want spec.replicas residual", search.Residual)
	}
}

func TestPlanMixedPushdownAndResidual(t *testing.T) {
	plan := mustPlan(t, `
		object.apiVersion == "v1" &&
		object.kind == "Pod" &&
		object.metadata.labels["app"] == "web" &&
		object.metadata.name.startsWith("web-")
	`)
	search := onlySearch(t, plan)
	if search.Native.GVK != podGVK {
		t.Fatalf("GVK = %s, want pod", search.Native.GVK)
	}
	if !search.Native.LabelSelector.Matches(labels.Set{"app": "web"}) {
		t.Fatalf("selector %q should match app=web", search.Native.RawLabelSelector)
	}
	if search.Exact || search.Residual == nil || !strings.Contains(search.Residual.Source, `startsWith("web-")`) {
		t.Fatalf("exact=%v residual=%#v, want startsWith residual", search.Exact, search.Residual)
	}
}

func TestPlanOROverNamespaces(t *testing.T) {
	plan := mustPlan(t, `
		object.apiVersion == "v1" &&
		object.kind == "Pod" &&
		(object.metadata.namespace == "prod" || object.metadata.namespace == "stage")
	`)
	if len(plan.Searches) != 2 {
		t.Fatalf("searches = %d, want 2", len(plan.Searches))
	}
	got := sortedNamespaces(plan)
	if strings.Join(got, ",") != "prod,stage" {
		t.Fatalf("namespaces = %v, want prod,stage", got)
	}
}

func TestPlanNamespaceInList(t *testing.T) {
	plan := mustPlan(t, `
		object.metadata.namespace in ["prod", "stage"] &&
		object.apiVersion == "v1" &&
		object.kind == "Pod"
	`)
	if len(plan.Searches) != 2 {
		t.Fatalf("searches = %d, want 2", len(plan.Searches))
	}
	got := sortedNamespaces(plan)
	if strings.Join(got, ",") != "prod,stage" {
		t.Fatalf("namespaces = %v, want prod,stage", got)
	}
}

func TestPlanOROverGVKs(t *testing.T) {
	plan := mustPlan(t, `
		(object.apiVersion == "v1" && object.kind == "Pod") ||
		(object.apiVersion == "apps/v1" && object.kind == "Deployment")
	`)
	if len(plan.Searches) != 2 {
		t.Fatalf("searches = %d, want 2", len(plan.Searches))
	}
	got := sortedKinds(plan)
	if strings.Join(got, ",") != "Deployment,Pod" {
		t.Fatalf("kinds = %v, want Deployment,Pod", got)
	}
}

func TestPlanFactoredOROverGVKs(t *testing.T) {
	plan := mustPlan(t, `
		object.metadata.labels["app"] == "web" &&
		(
			(object.apiVersion == "v1" && object.kind == "Pod") ||
			(object.apiVersion == "apps/v1" && object.kind == "Deployment")
		)
	`)
	if len(plan.Searches) != 2 {
		t.Fatalf("searches = %d, want 2", len(plan.Searches))
	}
	for _, search := range plan.Searches {
		if !search.Native.LabelSelector.Matches(labels.Set{"app": "web"}) {
			t.Fatalf("selector %q should match app=web", search.Native.RawLabelSelector)
		}
	}
}

func TestPlanFactoredOROverKindsWithoutAPIVersionWhenMapperCanResolve(t *testing.T) {
	plan := mustPlan(t, `
		object.metadata.labels["app"] == "web" &&
		(object.kind == "Pod" || object.kind == "Service")
	`)
	if len(plan.Searches) != 2 {
		t.Fatalf("searches = %d, want 2", len(plan.Searches))
	}
	got := sortedKinds(plan)
	if strings.Join(got, ",") != "Pod,Service" {
		t.Fatalf("kinds = %v, want Pod,Service", got)
	}
	for _, search := range plan.Searches {
		if !search.Native.ResourceKnown {
			t.Fatalf("search %s resource should be known", search.Native.GVK.Kind)
		}
		if !search.Native.LabelSelector.Matches(labels.Set{"app": "web"}) {
			t.Fatalf("selector %q should match app=web", search.Native.RawLabelSelector)
		}
	}
}

func TestPlanORWithResidualPerBranch(t *testing.T) {
	plan := mustPlan(t, `
		(
			object.apiVersion == "v1" &&
			object.kind == "Pod" &&
			object.metadata.name.startsWith("web-")
		) ||
		(
			object.apiVersion == "apps/v1" &&
			object.kind == "Deployment" &&
			object.spec.replicas > 3
		)
	`)
	if len(plan.Searches) != 2 {
		t.Fatalf("searches = %d, want 2", len(plan.Searches))
	}
	residuals := map[string]string{}
	for _, search := range plan.Searches {
		if search.Residual == nil {
			t.Fatalf("search for %s residual = nil, want residual", search.Native.GVK.Kind)
		}
		residuals[search.Native.GVK.Kind] = search.Residual.Source
	}
	if !strings.Contains(residuals["Pod"], `startsWith("web-")`) {
		t.Fatalf("pod residual = %q", residuals["Pod"])
	}
	if !strings.Contains(residuals["Deployment"], "object.spec.replicas > 3") {
		t.Fatalf("deployment residual = %q", residuals["Deployment"])
	}
}

func TestPlanNoUnsafeNameApproximation(t *testing.T) {
	search := onlySearch(t, mustPlan(t, `object.metadata.name.startsWith("web-")`))
	if search.Native.Name != "" {
		t.Fatalf("name = %q, want no name pushdown", search.Native.Name)
	}
	if search.Residual == nil || !strings.Contains(search.Residual.Source, `startsWith("web-")`) {
		t.Fatalf("residual = %#v, want startsWith residual", search.Residual)
	}
}

func TestPlanComplexNOTResidual(t *testing.T) {
	search := onlySearch(t, mustPlan(t, `!(object.metadata.name.startsWith("test-"))`))
	if search.Residual == nil || !strings.Contains(search.Residual.Source, `startsWith("test-")`) {
		t.Fatalf("residual = %#v, want complex NOT residual", search.Residual)
	}
}

func TestPlanMultipleNames(t *testing.T) {
	plan := mustPlan(t, `
		object.apiVersion == "v1" &&
		object.kind == "Pod" &&
		object.metadata.namespace == "default" &&
		(object.metadata.name == "a" || object.metadata.name == "b")
	`)
	if len(plan.Searches) != 2 {
		t.Fatalf("searches = %d, want 2", len(plan.Searches))
	}
	names := []string{plan.Searches[0].Native.Name, plan.Searches[1].Native.Name}
	sort.Strings(names)
	if strings.Join(names, ",") != "a,b" {
		t.Fatalf("names = %v, want a,b", names)
	}
}

func onlySearch(t *testing.T, plan *pushdown.Plan[kubepushdown.Query]) pushdown.Search[kubepushdown.Query] {
	t.Helper()
	if len(plan.Searches) != 1 {
		t.Fatalf("searches = %d, want 1", len(plan.Searches))
	}
	return plan.Searches[0]
}

func sortedNamespaces(plan *pushdown.Plan[kubepushdown.Query]) []string {
	out := make([]string, 0, len(plan.Searches))
	for _, search := range plan.Searches {
		out = append(out, search.Native.Namespace)
	}
	sort.Strings(out)
	return out
}

func sortedKinds(plan *pushdown.Plan[kubepushdown.Query]) []string {
	out := make([]string, 0, len(plan.Searches))
	for _, search := range plan.Searches {
		out = append(out, search.Native.GVK.Kind)
	}
	sort.Strings(out)
	return out
}
