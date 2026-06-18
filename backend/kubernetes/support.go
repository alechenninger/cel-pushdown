package kubernetes

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Scope describes whether a Kubernetes resource is namespaced.
type Scope int

const (
	UnknownScope Scope = iota
	Namespaced
	ClusterScoped
)

// ResourceScope reports Kubernetes resource scope for a GVK.
type ResourceScope interface {
	Scope(gvk schema.GroupVersionKind) Scope
}

// StaticResourceScope is a configured scope registry.
type StaticResourceScope map[schema.GroupVersionKind]Scope

func (s StaticResourceScope) Scope(gvk schema.GroupVersionKind) Scope {
	if scope, ok := s[gvk]; ok {
		return scope
	}
	return UnknownScope
}

type restMapperScope struct {
	mapper meta.RESTMapper
}

func (s restMapperScope) Scope(gvk schema.GroupVersionKind) Scope {
	if s.mapper == nil || gvk.Kind == "" {
		return UnknownScope
	}
	versions := []string{}
	if gvk.Version != "" {
		versions = append(versions, gvk.Version)
	}
	mapping, err := s.mapper.RESTMapping(gvk.GroupKind(), versions...)
	if err != nil || mapping == nil || mapping.Scope == nil {
		return UnknownScope
	}
	switch mapping.Scope.Name() {
	case meta.RESTScopeNameNamespace:
		return Namespaced
	case meta.RESTScopeNameRoot:
		return ClusterScoped
	default:
		return UnknownScope
	}
}

// FieldSupport reports whether a field path is selectable for a GVK.
type FieldSupport interface {
	SupportsField(gvk schema.GroupVersionKind, fieldPath string) bool
}

// MinimalFieldSupport supports Kubernetes metadata fields common to all resources.
type MinimalFieldSupport struct{}

func (MinimalFieldSupport) SupportsField(_ schema.GroupVersionKind, fieldPath string) bool {
	return fieldPath == "metadata.name" || fieldPath == "metadata.namespace"
}

// StaticFieldSupport supports configured selectable fields.
type StaticFieldSupport map[schema.GroupVersionKind][]string

func (s StaticFieldSupport) SupportsField(gvk schema.GroupVersionKind, fieldPath string) bool {
	for _, path := range s[gvk] {
		if path == fieldPath {
			return true
		}
	}
	return false
}

// BuiltinFieldSupport includes minimal fields and common built-in Pod selectors.
type BuiltinFieldSupport struct {
	Additional FieldSupport
}

func (b BuiltinFieldSupport) SupportsField(gvk schema.GroupVersionKind, fieldPath string) bool {
	if (MinimalFieldSupport{}).SupportsField(gvk, fieldPath) {
		return true
	}
	if b.Additional != nil && b.Additional.SupportsField(gvk, fieldPath) {
		return true
	}
	if gvk.Group == "" && gvk.Version == "v1" && gvk.Kind == "Pod" {
		switch fieldPath {
		case "spec.nodeName", "status.phase", "status.podIP", "status.hostIP", "spec.serviceAccountName":
			return true
		}
	}
	return false
}

// CombinedFieldSupport returns true when any member supports the field.
type CombinedFieldSupport []FieldSupport

func (c CombinedFieldSupport) SupportsField(gvk schema.GroupVersionKind, fieldPath string) bool {
	for _, support := range c {
		if support != nil && support.SupportsField(gvk, fieldPath) {
			return true
		}
	}
	return false
}
