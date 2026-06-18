package kubernetes

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	pushdown "cel-pushdown"
	"cel-pushdown/celutil"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic"
)

// KubernetesExecutor executes Kubernetes query plans serially with a dynamic client.
type KubernetesExecutor struct {
	Client             dynamic.Interface
	Mapper             meta.RESTMapper
	ResourceScope      ResourceScope
	AllowAllNamespaces bool
}

func (e *KubernetesExecutor) BackendName() string { return BackendName }

func (e *KubernetesExecutor) Execute(ctx context.Context, plan *pushdown.Plan[Query]) ([]pushdown.Candidate[*unstructured.Unstructured], error) {
	if plan == nil {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := []pushdown.Candidate[*unstructured.Unstructured]{}
	for _, search := range plan.Searches {
		candidates, err := e.ExecuteSearch(ctx, search)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			key := objectKey(candidate.Value)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, candidate)
		}
	}
	return out, nil
}

func (e *KubernetesExecutor) ExecuteSearch(ctx context.Context, search pushdown.Search[Query]) ([]pushdown.Candidate[*unstructured.Unstructured], error) {
	if e == nil || e.Client == nil {
		return nil, fmt.Errorf("kubernetes executor requires a dynamic client")
	}
	q := search.Native
	if q.GVR.Empty() || !q.ResourceKnown {
		return nil, fmt.Errorf("kubernetes query for search %q has no resolved GVR for GVK %s", search.ID, q.GVK.String())
	}
	scope := e.scope(q)
	if q.Namespace != "" && scope == ClusterScoped {
		return nil, nil
	}
	if q.Namespace != "" {
		resource := e.Client.Resource(q.GVR).Namespace(q.Namespace)
		if q.Name != "" {
			obj, err := resource.Get(ctx, q.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			return e.filterObjects(search.ID, q, []*unstructured.Unstructured{obj}), nil
		}
		list, err := resource.List(ctx, listOptions(q, false))
		if err != nil {
			return nil, err
		}
		return e.listCandidates(search.ID, q, list), nil
	}

	if q.Name != "" && scope == ClusterScoped {
		obj, err := e.Client.Resource(q.GVR).Get(ctx, q.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return e.filterObjects(search.ID, q, []*unstructured.Unstructured{obj}), nil
	}
	if q.Name != "" {
		if e.AllowAllNamespaces || q.AllNamespaces {
			list, err := e.Client.Resource(q.GVR).List(ctx, listOptions(q, true))
			if err != nil {
				return nil, err
			}
			return e.listCandidates(search.ID, q, list), nil
		}
		return nil, fmt.Errorf("kubernetes query for search %q has a name but no namespace; all-namespaces listing is disabled", search.ID)
	}

	if scope == ClusterScoped {
		list, err := e.Client.Resource(q.GVR).List(ctx, listOptions(q, false))
		if err != nil {
			return nil, err
		}
		return e.listCandidates(search.ID, q, list), nil
	}
	if e.AllowAllNamespaces || q.AllNamespaces {
		list, err := e.Client.Resource(q.GVR).List(ctx, listOptions(q, false))
		if err != nil {
			return nil, err
		}
		return e.listCandidates(search.ID, q, list), nil
	}
	return nil, fmt.Errorf("kubernetes query for search %q is missing namespace for namespaced or unknown-scope resource %s", search.ID, q.GVK.String())
}

func (e *KubernetesExecutor) ExecuteAndFilter(ctx context.Context, plan *pushdown.Plan[Query]) ([]unstructured.Unstructured, error) {
	if plan == nil {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := []unstructured.Unstructured{}
	for _, search := range plan.Searches {
		candidates, err := e.ExecuteSearch(ctx, search)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			ok, err := EvalResidual(ctx, search.Residual, candidate.Value)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			key := objectKey(candidate.Value)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, *candidate.Value.DeepCopy())
		}
	}
	return out, nil
}

// ExecuteAndFilter executes a plan and evaluates residual CEL with permissive all-namespaces listing.
func ExecuteAndFilter(ctx context.Context, client dynamic.Interface, plan *pushdown.Plan[Query]) ([]unstructured.Unstructured, error) {
	return (&KubernetesExecutor{Client: client, AllowAllNamespaces: true}).ExecuteAndFilter(ctx, plan)
}

// EvalResidual evaluates a residual CEL predicate against an unstructured Kubernetes object.
func EvalResidual(ctx context.Context, pred *pushdown.CELPredicate, obj *unstructured.Unstructured) (bool, error) {
	if pred == nil {
		return true, nil
	}
	return celutil.EvalPredicate(ctx, pred, map[string]any{"object": objectForCEL(obj)})
}

func (e *KubernetesExecutor) scope(q Query) Scope {
	if e.ResourceScope != nil {
		if scope := e.ResourceScope.Scope(q.GVK); scope != UnknownScope {
			return scope
		}
	}
	if e.Mapper != nil {
		if scope := (restMapperScope{mapper: e.Mapper}).Scope(q.GVK); scope != UnknownScope {
			return scope
		}
	}
	return UnknownScope
}

func listOptions(q Query, forceName bool) metav1.ListOptions {
	labelSelector := q.RawLabelSelector
	if labelSelector == "" && q.LabelSelector != nil && !q.LabelSelector.Empty() {
		labelSelector = q.LabelSelector.String()
	}
	fieldSelector := q.RawFieldSelector
	selector := q.FieldSelector
	if selector == nil {
		selector = fields.Everything()
	}
	if forceName && q.Name != "" {
		selector = fields.AndSelectors(selector, fields.OneTermEqualSelector("metadata.name", q.Name))
		fieldSelector = selector.String()
	} else if fieldSelector == "" && !selector.Empty() {
		fieldSelector = selector.String()
	}
	return metav1.ListOptions{
		LabelSelector: labelSelector,
		FieldSelector: fieldSelector,
	}
}

func (e *KubernetesExecutor) listCandidates(searchID string, q Query, list *unstructured.UnstructuredList) []pushdown.Candidate[*unstructured.Unstructured] {
	if list == nil {
		return nil
	}
	objects := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		obj := list.Items[i].DeepCopy()
		objects = append(objects, obj)
	}
	return e.filterObjects(searchID, q, objects)
}

func (e *KubernetesExecutor) filterObjects(searchID string, q Query, objects []*unstructured.Unstructured) []pushdown.Candidate[*unstructured.Unstructured] {
	out := []pushdown.Candidate[*unstructured.Unstructured]{}
	for _, obj := range objects {
		if queryMatchesObject(q, obj) {
			out = append(out, pushdown.Candidate[*unstructured.Unstructured]{
				SearchID: searchID,
				Value:    obj,
			})
		}
	}
	return out
}

func queryMatchesObject(q Query, obj *unstructured.Unstructured) bool {
	if obj == nil {
		return false
	}
	gvk := obj.GroupVersionKind()
	if q.GVK.Group != "" && q.GVK.Group != gvk.Group {
		return false
	}
	if q.GVK.Version != "" && q.GVK.Version != gvk.Version {
		return false
	}
	if q.GVK.Kind != "" && q.GVK.Kind != gvk.Kind {
		return false
	}
	if q.Namespace != "" && q.Namespace != obj.GetNamespace() {
		return false
	}
	if q.Name != "" && q.Name != obj.GetName() {
		return false
	}
	if q.LabelSelector != nil && !q.LabelSelector.Empty() {
		if !q.LabelSelector.Matches(labels.Set(obj.GetLabels())) {
			return false
		}
	}
	if q.FieldSelector != nil && !q.FieldSelector.Empty() {
		if !q.FieldSelector.Matches(fieldSetForSelector(q.FieldSelector, obj)) {
			return false
		}
	}
	return true
}

func fieldSetForSelector(selector fields.Selector, obj *unstructured.Unstructured) fields.Set {
	set := fields.Set{}
	for _, req := range selector.Requirements() {
		set[req.Field] = objectFieldValue(obj, req.Field)
	}
	return set
}

func objectFieldValue(obj *unstructured.Unstructured, fieldPath string) string {
	switch fieldPath {
	case "metadata.name":
		return obj.GetName()
	case "metadata.namespace":
		return obj.GetNamespace()
	}
	parts := strings.Split(fieldPath, ".")
	val, found, _ := unstructured.NestedFieldNoCopy(obj.Object, parts...)
	if !found || val == nil {
		return ""
	}
	switch x := val.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprint(x)
	}
}

func objectForCEL(obj *unstructured.Unstructured) map[string]any {
	if obj == nil {
		return map[string]any{}
	}
	out := obj.UnstructuredContent()
	if obj.GetAPIVersion() != "" {
		out["apiVersion"] = obj.GetAPIVersion()
	}
	if obj.GetKind() != "" {
		out["kind"] = obj.GetKind()
	}
	metadata, _ := out["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
		out["metadata"] = metadata
	}
	if obj.GetName() != "" {
		metadata["name"] = obj.GetName()
	}
	if obj.GetNamespace() != "" {
		metadata["namespace"] = obj.GetNamespace()
	}
	labelsMap := map[string]any{}
	for k, v := range obj.GetLabels() {
		labelsMap[k] = v
	}
	metadata["labels"] = labelsMap
	return out
}

func objectKey(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	gvk := obj.GroupVersionKind()
	return gvk.Group + "/" + gvk.Version + "/" + gvk.Kind + "|" + obj.GetNamespace() + "|" + obj.GetName()
}
