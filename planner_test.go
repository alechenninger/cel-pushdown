package celkube

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestPlannerPlan(t *testing.T) {
	t.Parallel()

	podGVK := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

	tests := []struct {
		name         string
		expr         string
		fieldSupport FieldSupport
		want         func(t *testing.T, plan *Plan)
	}{
		{
			name: "gvk namespace name",
			expr: `object.apiVersion == "v1" &&
object.kind == "Pod" &&
object.metadata.namespace == "default" &&
object.metadata.name == "nginx"`,
			fieldSupport: MinimalFieldSupport{},
			want: func(t *testing.T, plan *Plan) {
				if !plan.Resource.Known || plan.Resource.GroupVersionKind != podGVK {
					t.Fatalf("unexpected resource plan: %#v", plan.Resource)
				}
				if plan.Resource.GroupVersionResource != podGVR {
					t.Fatalf("unexpected gvr: %#v", plan.Resource.GroupVersionResource)
				}
				if plan.Scope.Namespace != "default" || plan.Scope.Name != "nginx" {
					t.Fatalf("unexpected scope: %#v", plan.Scope)
				}
				if !plan.Exact || plan.Residual != nil {
					t.Fatalf("expected exact plan, got residual %#v", plan.Residual)
				}
			},
		},
		{
			name:         "label equality",
			expr:         `object.metadata.labels["app"] == "web"`,
			fieldSupport: MinimalFieldSupport{},
			want: func(t *testing.T, plan *Plan) {
				if plan.Selectors.RawLabelSelector != "app=web" {
					t.Fatalf("unexpected label selector: %q", plan.Selectors.RawLabelSelector)
				}
				if plan.Residual != nil {
					t.Fatalf("unexpected residual: %#v", plan.Residual)
				}
			},
		},
		{
			name:         "label in",
			expr:         `object.metadata.labels["app"] in ["web", "api"]`,
			fieldSupport: MinimalFieldSupport{},
			want: func(t *testing.T, plan *Plan) {
				if plan.Selectors.RawLabelSelector != "app in (api,web)" && plan.Selectors.RawLabelSelector != "app in (web,api)" {
					t.Fatalf("unexpected label selector: %q", plan.Selectors.RawLabelSelector)
				}
			},
		},
		{
			name:         "label exists",
			expr:         `"app" in object.metadata.labels`,
			fieldSupport: MinimalFieldSupport{},
			want: func(t *testing.T, plan *Plan) {
				if plan.Selectors.RawLabelSelector != "app" {
					t.Fatalf("unexpected label selector: %q", plan.Selectors.RawLabelSelector)
				}
			},
		},
		{
			name:         "label missing",
			expr:         `!("app" in object.metadata.labels)`,
			fieldSupport: MinimalFieldSupport{},
			want: func(t *testing.T, plan *Plan) {
				if plan.Selectors.RawLabelSelector != "!app" {
					t.Fatalf("unexpected label selector: %q", plan.Selectors.RawLabelSelector)
				}
			},
		},
		{
			name:         "field selector allowed",
			expr:         `object.apiVersion == "v1" && object.kind == "Pod" && object.status.phase == "Running"`,
			fieldSupport: BuiltinFieldSupport{},
			want: func(t *testing.T, plan *Plan) {
				if plan.Selectors.RawFieldSelector != "status.phase=Running" {
					t.Fatalf("unexpected field selector: %q", plan.Selectors.RawFieldSelector)
				}
				if plan.Residual != nil {
					t.Fatalf("unexpected residual: %#v", plan.Residual)
				}
			},
		},
		{
			name:         "field selector unsupported",
			expr:         `object.spec.replicas == 3`,
			fieldSupport: MinimalFieldSupport{},
			want: func(t *testing.T, plan *Plan) {
				if plan.Selectors.RawFieldSelector != "" {
					t.Fatalf("unexpected field selector: %q", plan.Selectors.RawFieldSelector)
				}
				if plan.Residual == nil || !strings.Contains(plan.Residual.Source, `object.spec.replicas == 3`) {
					t.Fatalf("unexpected residual: %#v", plan.Residual)
				}
			},
		},
		{
			name:         "mixed pushdown and residual",
			expr:         `object.metadata.labels["app"] == "web" && object.metadata.name.startsWith("web-")`,
			fieldSupport: MinimalFieldSupport{},
			want: func(t *testing.T, plan *Plan) {
				if plan.Selectors.RawLabelSelector != "app=web" {
					t.Fatalf("unexpected label selector: %q", plan.Selectors.RawLabelSelector)
				}
				if plan.Residual == nil || plan.Residual.Source != `object.metadata.name.startsWith("web-")` {
					t.Fatalf("unexpected residual: %#v", plan.Residual)
				}
				if plan.Exact {
					t.Fatal("expected partial plan")
				}
			},
		},
		{
			name:         "or not supported",
			expr:         `object.metadata.namespace == "a" || object.metadata.namespace == "b"`,
			fieldSupport: MinimalFieldSupport{},
			want: func(t *testing.T, plan *Plan) {
				if plan.Scope.Namespace != "" {
					t.Fatalf("unexpected namespace pushdown: %#v", plan.Scope)
				}
				if plan.Residual == nil || plan.Residual.Source != `object.metadata.namespace == "a" || object.metadata.namespace == "b"` {
					t.Fatalf("unexpected residual: %#v", plan.Residual)
				}
			},
		},
		{
			name:         "correctness guard",
			expr:         `object.metadata.name.startsWith("web-")`,
			fieldSupport: MinimalFieldSupport{},
			want: func(t *testing.T, plan *Plan) {
				if plan.Scope.Name != "" || plan.Selectors.RawFieldSelector != "" {
					t.Fatalf("unexpected pushdown: %#v %#v", plan.Scope, plan.Selectors)
				}
				if plan.Residual == nil || plan.Residual.Source != `object.metadata.name.startsWith("web-")` {
					t.Fatalf("unexpected residual: %#v", plan.Residual)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			planner, err := NewPlanner(PlanOptions{
				FieldSupport:        tt.fieldSupport,
				ResourceResolver:    staticResolver{mapping: map[schema.GroupVersionKind]schema.GroupVersionResource{podGVK: podGVR}},
				PreserveOriginalCEL: true,
			})
			if err != nil {
				t.Fatalf("NewPlanner() error = %v", err)
			}
			plan, err := planner.Plan(context.Background(), tt.expr)
			if err != nil {
				t.Fatalf("Plan() error = %v", err)
			}
			tt.want(t, plan)
		})
	}
}

func TestEvalResidual(t *testing.T) {
	t.Parallel()

	planner, err := NewPlanner(PlanOptions{FieldSupport: MinimalFieldSupport{}})
	if err != nil {
		t.Fatalf("NewPlanner() error = %v", err)
	}

	plan, err := planner.Plan(context.Background(), `object.metadata.name.startsWith("web-")`)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	cases := []struct {
		name string
		obj  map[string]any
		want bool
	}{
		{name: "match", obj: map[string]any{"metadata": map[string]any{"name": "web-1"}}, want: true},
		{name: "no match", obj: map[string]any{"metadata": map[string]any{"name": "api-1"}}, want: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := planner.EvalResidual(context.Background(), plan, tc.obj)
			if err != nil {
				t.Fatalf("EvalResidual() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("EvalResidual() = %v, want %v", got, tc.want)
			}
		})
	}
}

type staticResolver struct {
	mapping map[schema.GroupVersionKind]schema.GroupVersionResource
}

func (r staticResolver) Resolve(gvk schema.GroupVersionKind) (schema.GroupVersionResource, error) {
	gvr, ok := r.mapping[gvk]
	if !ok {
		return schema.GroupVersionResource{}, fmt.Errorf("no mapping for %s", gvk.String())
	}
	return gvr, nil
}
