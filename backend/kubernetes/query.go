package kubernetes

import (
	"encoding/json"
	"strings"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const BackendName = "kubernetes"

// Query is the Kubernetes native query produced by the planner.
type Query struct {
	GVK           schema.GroupVersionKind     `json:"gvk"`
	GVR           schema.GroupVersionResource `json:"gvr"`
	ResourceKnown bool                        `json:"resourceKnown"`

	Namespace        string `json:"namespace,omitempty"`
	AllNamespaces    bool   `json:"allNamespaces,omitempty"`
	Name             string `json:"name,omitempty"`
	LabelSelector    labels.Selector
	FieldSelector    fields.Selector
	RawLabelSelector string `json:"labelSelector,omitempty"`
	RawFieldSelector string `json:"fieldSelector,omitempty"`
}

func (q Query) BackendName() string { return BackendName }

func (q Query) String() string {
	parts := []string{}
	if !q.GVK.Empty() {
		parts = append(parts, "gvk="+q.GVK.String())
	}
	if !q.GVR.Empty() {
		parts = append(parts, "gvr="+q.GVR.String())
	}
	if q.Namespace != "" {
		parts = append(parts, "namespace="+q.Namespace)
	} else if q.AllNamespaces {
		parts = append(parts, "allNamespaces=true")
	}
	if q.Name != "" {
		parts = append(parts, "name="+q.Name)
	}
	if q.RawLabelSelector != "" {
		parts = append(parts, "labels="+q.RawLabelSelector)
	}
	if q.RawFieldSelector != "" {
		parts = append(parts, "fields="+q.RawFieldSelector)
	}
	if len(parts) == 0 {
		return "kubernetes:<all>"
	}
	return "kubernetes:" + strings.Join(parts, ",")
}

func newQuery() Query {
	return Query{
		LabelSelector: labels.Everything(),
		FieldSelector: fields.Everything(),
	}
}

// MarshalJSON renders selector interfaces as stable selector strings.
func (q Query) MarshalJSON() ([]byte, error) {
	type wireQuery struct {
		APIVersion    string `json:"apiVersion,omitempty"`
		Kind          string `json:"kind,omitempty"`
		Group         string `json:"group,omitempty"`
		Version       string `json:"version,omitempty"`
		Resource      string `json:"resource,omitempty"`
		ResourceKnown bool   `json:"resourceKnown"`
		Namespace     string `json:"namespace,omitempty"`
		AllNamespaces bool   `json:"allNamespaces,omitempty"`
		Name          string `json:"name,omitempty"`
		LabelSelector string `json:"labelSelector,omitempty"`
		FieldSelector string `json:"fieldSelector,omitempty"`
	}
	out := wireQuery{
		APIVersion:    q.GVK.GroupVersion().String(),
		Kind:          q.GVK.Kind,
		Group:         q.GVR.Group,
		Version:       q.GVR.Version,
		Resource:      q.GVR.Resource,
		ResourceKnown: q.ResourceKnown,
		Namespace:     q.Namespace,
		AllNamespaces: q.AllNamespaces,
		Name:          q.Name,
		LabelSelector: q.RawLabelSelector,
		FieldSelector: q.RawFieldSelector,
	}
	if q.GVK.Empty() {
		out.APIVersion = ""
	}
	return json.Marshal(out)
}

func finalizeSelectors(q *Query, labelReqs []labels.Requirement, fieldSelectors []fields.Selector) {
	q.LabelSelector = labels.NewSelector()
	if len(labelReqs) == 0 {
		q.LabelSelector = labels.Everything()
		q.RawLabelSelector = ""
	} else {
		q.LabelSelector = q.LabelSelector.Add(labelReqs...)
		q.RawLabelSelector = q.LabelSelector.String()
	}
	if len(fieldSelectors) == 0 {
		q.FieldSelector = fields.Everything()
		q.RawFieldSelector = ""
	} else {
		q.FieldSelector = fields.AndSelectors(fieldSelectors...)
		q.RawFieldSelector = q.FieldSelector.String()
	}
}
