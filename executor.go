package celkube

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

func (p *Planner) Execute(ctx context.Context, client dynamic.Interface, plan *Plan) ([]unstructured.Unstructured, error) {
	if plan == nil {
		return nil, fmt.Errorf("plan is nil")
	}
	if client == nil {
		return nil, fmt.Errorf("dynamic client is nil")
	}
	if plan.Resource.GroupVersionResource.Empty() {
		return nil, fmt.Errorf("plan resource is unresolved; execution requires a GroupVersionResource")
	}
	if plan.Scope.Namespace == "" && !p.opts.AllowAllNamespaces {
		return nil, fmt.Errorf("all-namespaces execution is disabled")
	}

	namespaceable := client.Resource(plan.Resource.GroupVersionResource)
	var resource dynamic.ResourceInterface = namespaceable
	if plan.Scope.Namespace != "" {
		resource = namespaceable.Namespace(plan.Scope.Namespace)
	}

	if plan.Scope.Namespace != "" && plan.Scope.Name != "" {
		obj, err := resource.Get(ctx, plan.Scope.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return []unstructured.Unstructured{*obj}, nil
	}

	list, err := resource.List(ctx, metav1.ListOptions{
		LabelSelector: plan.Selectors.RawLabelSelector,
		FieldSelector: plan.Selectors.RawFieldSelector,
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (p *Planner) ExecuteAndFilter(ctx context.Context, client dynamic.Interface, plan *Plan) ([]unstructured.Unstructured, error) {
	items, err := p.Execute(ctx, client, plan)
	if err != nil {
		return nil, err
	}
	if plan.Residual == nil {
		return items, nil
	}
	filtered := make([]unstructured.Unstructured, 0, len(items))
	for _, item := range items {
		object := item.Object
		if object["apiVersion"] == nil && item.GetAPIVersion() != "" {
			object["apiVersion"] = item.GetAPIVersion()
		}
		if object["kind"] == nil && item.GetKind() != "" {
			object["kind"] = item.GetKind()
		}
		matched, err := p.EvalResidual(ctx, plan, object)
		if err != nil {
			return nil, err
		}
		if matched {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}
