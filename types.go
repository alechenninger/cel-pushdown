package celkube

import (
	"github.com/google/cel-go/cel"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type Planner struct {
	env  *cel.Env
	opts PlanOptions
}

type Plan struct {
	OriginalExpression string
	Resource           ResourcePlan
	Scope              ScopePlan
	Selectors          SelectorPlan
	Residual           *ResidualPlan
	Exact              bool
	Warnings           []Warning
}

type ResourcePlan struct {
	Group                string
	Version              string
	Kind                 string
	Resource             string
	GroupVersionResource schema.GroupVersionResource
	GroupVersionKind     schema.GroupVersionKind
	Known                bool
}

type ScopePlan struct {
	Namespace     string
	Name          string
	AllNamespaces bool
}

type SelectorPlan struct {
	Label            labels.Selector
	Field            fields.Selector
	RawLabelSelector string
	RawFieldSelector string
}

type ResidualPlan struct {
	Source  string
	Ast     *cel.Ast
	Program cel.Program
}

type Warning struct {
	Message string
}

type PlanOptions struct {
	AllowMultiQuery     bool
	AllowAllNamespaces  bool
	FieldSupport        FieldSupport
	ResourceResolver    ResourceResolver
	PreserveOriginalCEL bool
}

type FieldSupport interface {
	SupportsField(gvk schema.GroupVersionKind, fieldPath string) bool
}

type ResourceResolver interface {
	Resolve(schema.GroupVersionKind) (schema.GroupVersionResource, error)
}
