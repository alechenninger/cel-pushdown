package kubernetes

import (
	"reflect"
	"strconv"
	"strings"

	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
)

type objectPath struct {
	fields []string
	key    string
	hasKey bool
}

func (p objectPath) fieldPath() string {
	return strings.Join(p.fields, ".")
}

func (p objectPath) equals(fields ...string) bool {
	if p.hasKey || len(p.fields) != len(fields) {
		return false
	}
	for i := range fields {
		if p.fields[i] != fields[i] {
			return false
		}
	}
	return true
}

func (p objectPath) isLabelsMap() bool {
	return p.equals("metadata", "labels")
}

func (p objectPath) isLabelKey() bool {
	return p.hasKey && len(p.fields) == 2 && p.fields[0] == "metadata" && p.fields[1] == "labels"
}

func exprPath(expr celast.Expr) (objectPath, bool) {
	switch expr.Kind() {
	case celast.IdentKind:
		if expr.AsIdent() == "object" {
			return objectPath{}, true
		}
		return objectPath{}, false
	case celast.SelectKind:
		sel := expr.AsSelect()
		base, ok := exprPath(sel.Operand())
		if !ok {
			return objectPath{}, false
		}
		base.fields = append(base.fields, sel.FieldName())
		return base, true
	case celast.CallKind:
		call := expr.AsCall()
		if call.FunctionName() != operators.Index && call.FunctionName() != operators.OptIndex {
			return objectPath{}, false
		}
		args := call.Args()
		if len(args) != 2 {
			return objectPath{}, false
		}
		base, ok := exprPath(args[0])
		if !ok || base.hasKey {
			return objectPath{}, false
		}
		key, ok := literalString(args[1])
		if !ok {
			return objectPath{}, false
		}
		base.key = key
		base.hasKey = true
		return base, true
	default:
		return objectPath{}, false
	}
}

func literalString(expr celast.Expr) (string, bool) {
	if expr.Kind() != celast.LiteralKind {
		return "", false
	}
	v, ok := expr.AsLiteral().Value().(string)
	return v, ok
}

func literalValueString(expr celast.Expr) (string, bool) {
	if expr.Kind() != celast.LiteralKind {
		return "", false
	}
	v := expr.AsLiteral().Value()
	switch x := v.(type) {
	case string:
		return x, true
	case int64:
		return strconv.FormatInt(x, 10), true
	case int32:
		return strconv.FormatInt(int64(x), 10), true
	case int:
		return strconv.Itoa(x), true
	case uint64:
		return strconv.FormatUint(x, 10), true
	case uint:
		return strconv.FormatUint(uint64(x), 10), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	default:
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return strconv.FormatInt(rv.Int(), 10), true
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return strconv.FormatUint(rv.Uint(), 10), true
		case reflect.Float32, reflect.Float64:
			return strconv.FormatFloat(rv.Float(), 'f', -1, 64), true
		}
	}
	return "", false
}

func literalStringList(expr celast.Expr) ([]string, bool) {
	if expr.Kind() != celast.ListKind {
		return nil, false
	}
	list := expr.AsList()
	out := make([]string, 0, list.Size())
	for _, elem := range list.Elements() {
		s, ok := literalString(elem)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func binaryCall(expr celast.Expr, op string) (celast.Expr, celast.Expr, bool) {
	if expr.Kind() != celast.CallKind {
		return nil, nil, false
	}
	call := expr.AsCall()
	if call.FunctionName() != op {
		return nil, nil, false
	}
	args := call.Args()
	if len(args) != 2 {
		return nil, nil, false
	}
	return args[0], args[1], true
}

func notArg(expr celast.Expr) (celast.Expr, bool) {
	if expr.Kind() != celast.CallKind {
		return nil, false
	}
	call := expr.AsCall()
	if call.FunctionName() != operators.LogicalNot {
		return nil, false
	}
	args := call.Args()
	if len(args) != 1 {
		return nil, false
	}
	return args[0], true
}

func matchPathStringSet(expr celast.Expr, fields ...string) ([]string, bool) {
	left, right, ok := binaryCall(expr, operators.Equals)
	if ok {
		if p, pathOK := exprPath(left); pathOK && p.equals(fields...) {
			if s, litOK := literalString(right); litOK {
				return []string{s}, true
			}
		}
		if p, pathOK := exprPath(right); pathOK && p.equals(fields...) {
			if s, litOK := literalString(left); litOK {
				return []string{s}, true
			}
		}
	}
	left, right, ok = binaryCall(expr, operators.In)
	if !ok {
		left, right, ok = binaryCall(expr, operators.OldIn)
	}
	if ok {
		if p, pathOK := exprPath(left); pathOK && p.equals(fields...) {
			if vals, listOK := literalStringList(right); listOK {
				return vals, true
			}
		}
	}
	return nil, false
}

func parseAPIVersion(apiVersion string) (group, version string, ok bool) {
	parts := strings.Split(apiVersion, "/")
	switch len(parts) {
	case 1:
		if parts[0] == "" {
			return "", "", false
		}
		return "", parts[0], true
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		return parts[0], parts[1], true
	default:
		return "", "", false
	}
}

func comparisonOperator(expr celast.Expr) (left celast.Expr, right celast.Expr, op string, ok bool) {
	for _, candidate := range []string{operators.Equals, operators.NotEquals} {
		l, r, match := binaryCall(expr, candidate)
		if match {
			return l, r, candidate, true
		}
	}
	return nil, nil, "", false
}
