package celkube

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type MinimalFieldSupport struct{}

func (MinimalFieldSupport) SupportsField(_ schema.GroupVersionKind, fieldPath string) bool {
	switch fieldPath {
	case "metadata.name", "metadata.namespace":
		return true
	default:
		return false
	}
}

type StaticFieldSupport struct {
	Global []string
	ByGVK  map[schema.GroupVersionKind][]string
}

func (s StaticFieldSupport) SupportsField(gvk schema.GroupVersionKind, fieldPath string) bool {
	for _, candidate := range s.Global {
		if candidate == fieldPath {
			return true
		}
	}
	for _, candidate := range s.ByGVK[gvk] {
		if candidate == fieldPath {
			return true
		}
	}
	return false
}

type BuiltinFieldSupport struct{}

func (BuiltinFieldSupport) SupportsField(gvk schema.GroupVersionKind, fieldPath string) bool {
	if (MinimalFieldSupport{}).SupportsField(gvk, fieldPath) {
		return true
	}
	podGVK := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
	if gvk == podGVK {
		switch fieldPath {
		case "spec.nodeName", "status.phase":
			return true
		}
	}
	return false
}

type RESTMapperResolver struct {
	Mapper meta.RESTMapper
}

func (r RESTMapperResolver) Resolve(gvk schema.GroupVersionKind) (schema.GroupVersionResource, error) {
	if r.Mapper == nil {
		return schema.GroupVersionResource{}, fmt.Errorf("rest mapper is nil")
	}
	mapping, err := r.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	return mapping.Resource, nil
}
