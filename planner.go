package celpushdown

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
)

func NewPlanner(opts PlanOptions) (*Planner, error) {
	if opts.FieldSupport == nil {
		opts.FieldSupport = MinimalFieldSupport{}
	}
	env, err := cel.NewEnv(cel.Variable("object", cel.DynType))
	if err != nil {
		return nil, err
	}
	return &Planner{env: env, opts: opts}, nil
}

func (p *Planner) Plan(ctx context.Context, expr string) (*Plan, error) {
	_ = ctx

	parsed, issues := p.env.Parse(expr)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	_, issues = p.env.Check(parsed)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}

	native := parsed.NativeRep()
	state := newConstraintState()
	var residualParts []string

	for _, conjunct := range splitConjuncts(native.Expr(), native.SourceInfo(), expr) {
		pushed, warning := p.tryPushConjunct(state, conjunct.expr)
		if warning != "" {
			state.warnings = append(state.warnings, Warning{Message: warning})
		}
		if !pushed {
			residualParts = append(residualParts, conjunct.source)
		}
	}

	plan := &Plan{
		Exact: true,
		Selectors: SelectorPlan{
			Label: labels.Everything(),
			Field: fields.Everything(),
		},
		Warnings: append([]Warning(nil), state.warnings...),
	}
	if p.opts.PreserveOriginalCEL {
		plan.OriginalExpression = expr
	}

	p.populateResourcePlan(plan, state)
	plan.Scope.Namespace = state.namespace
	plan.Scope.Name = state.name
	plan.Scope.AllNamespaces = state.namespace == ""

	if len(state.labelRequirements) > 0 {
		selector := labels.NewSelector()
		selector = selector.Add(state.labelRequirements...)
		plan.Selectors.Label = selector
		plan.Selectors.RawLabelSelector = selector.String()
	}
	if len(state.fieldSelectors) > 0 {
		selector := fields.AndSelectors(state.fieldSelectors...)
		plan.Selectors.Field = selector
		plan.Selectors.RawFieldSelector = selector.String()
	}

	if len(residualParts) > 0 {
		residualSource := joinResiduals(residualParts)
		residualAst, program, err := p.compileResidual(residualSource)
		if err != nil {
			return nil, err
		}
		plan.Residual = &ResidualPlan{
			Source:  residualSource,
			Ast:     residualAst,
			Program: program,
		}
		plan.Exact = false
	}

	if state.kind != "" && state.apiVersion == "" {
		plan.Warnings = append(plan.Warnings, Warning{Message: "kind constraint found without apiVersion; resource resolution requires both"})
	}
	if state.apiVersion != "" && state.kind == "" {
		plan.Warnings = append(plan.Warnings, Warning{Message: "apiVersion constraint found without kind; resource resolution requires both"})
	}

	return plan, nil
}

func (p *Planner) EvalResidual(ctx context.Context, plan *Plan, obj map[string]any) (bool, error) {
	_ = ctx

	if plan == nil || plan.Residual == nil {
		return true, nil
	}
	activationObject := map[string]any{}
	for k, v := range obj {
		activationObject[k] = v
	}

	out, _, err := plan.Residual.Program.Eval(map[string]any{"object": activationObject})
	if err != nil {
		return false, err
	}
	native, err := out.ConvertToNative(reflect.TypeOf(true))
	if err != nil {
		return false, err
	}
	matched, ok := native.(bool)
	if !ok {
		return false, fmt.Errorf("residual expression returned %T, want bool", native)
	}
	return matched, nil
}

type conjunct struct {
	expr   celast.Expr
	source string
}

type constraintState struct {
	apiVersion        string
	kind              string
	namespace         string
	name              string
	labelRequirements []labels.Requirement
	fieldSelectors    []fields.Selector
	warnings          []Warning
}

func newConstraintState() *constraintState {
	return &constraintState{}
}

func splitConjuncts(expr celast.Expr, info *celast.SourceInfo, original string) []conjunct {
	exprs := flattenConjuncts(expr)
	sources := splitTopLevelAnd(original)
	if len(exprs) == len(sources) {
		out := make([]conjunct, 0, len(exprs))
		for i := range exprs {
			out = append(out, conjunct{expr: exprs[i], source: strings.TrimSpace(sources[i])})
		}
		return out
	}
	return []conjunct{{expr: expr, source: expressionSource(expr, info, original)}}
}

func flattenConjuncts(expr celast.Expr) []celast.Expr {
	if !isCall(expr, operators.LogicalAnd) {
		return []celast.Expr{expr}
	}
	args := expr.AsCall().Args()
	out := flattenConjuncts(args[0])
	out = append(out, flattenConjuncts(args[1])...)
	return out
}

func expressionSource(expr celast.Expr, info *celast.SourceInfo, original string) string {
	if info != nil {
		if rng, ok := info.GetOffsetRange(expr.ID()); ok {
			start := maxInt(0, int(rng.Start))
			stop := minInt(len(original), int(rng.Stop)+1)
			if start < stop {
				return strings.TrimSpace(original[start:stop])
			}
		}
	}
	return strings.TrimSpace(original)
}

func splitTopLevelAnd(source string) []string {
	var parts []string
	start := 0
	depth := 0
	inString := false
	escaped := false
	for i := 0; i < len(source); i++ {
		ch := source[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case '&':
			if depth == 0 && i+1 < len(source) && source[i+1] == '&' {
				parts = append(parts, source[start:i])
				start = i + 2
				i++
			}
		}
	}
	parts = append(parts, source[start:])
	return parts
}

func joinResiduals(parts []string) string {
	if len(parts) == 1 {
		return strings.TrimSpace(parts[0])
	}
	wrapped := make([]string, 0, len(parts))
	for _, part := range parts {
		wrapped = append(wrapped, "("+strings.TrimSpace(part)+")")
	}
	return strings.Join(wrapped, " && ")
}

func (p *Planner) compileResidual(source string) (*cel.Ast, cel.Program, error) {
	parsed, issues := p.env.Parse(source)
	if issues != nil && issues.Err() != nil {
		return nil, nil, issues.Err()
	}
	checked, issues := p.env.Check(parsed)
	if issues != nil && issues.Err() != nil {
		return nil, nil, issues.Err()
	}
	program, err := p.env.Program(checked)
	if err != nil {
		return nil, nil, err
	}
	return checked, program, nil
}

func (p *Planner) populateResourcePlan(plan *Plan, state *constraintState) {
	if state.apiVersion == "" || state.kind == "" {
		return
	}
	gv, err := schema.ParseGroupVersion(state.apiVersion)
	if err != nil {
		plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf("could not parse apiVersion %q: %v", state.apiVersion, err)})
		return
	}
	gvk := gv.WithKind(state.kind)
	plan.Resource = ResourcePlan{
		Group:            gvk.Group,
		Version:          gvk.Version,
		Kind:             gvk.Kind,
		GroupVersionKind: gvk,
		Known:            true,
	}
	if p.opts.ResourceResolver == nil {
		return
	}
	gvr, err := p.opts.ResourceResolver.Resolve(gvk)
	if err != nil {
		plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf("could not resolve %s: %v", gvk.String(), err)})
		return
	}
	plan.Resource.Resource = gvr.Resource
	plan.Resource.GroupVersionResource = gvr
}

func (p *Planner) tryPushConjunct(state *constraintState, expr celast.Expr) (bool, string) {
	if expr.Kind() != celast.CallKind {
		return false, ""
	}
	call := expr.AsCall()
	switch call.FunctionName() {
	case operators.Equals, operators.NotEquals:
		return p.pushComparison(state, call.FunctionName(), call.Args()[0], call.Args()[1])
	case operators.In, operators.OldIn:
		return p.pushIn(state, call.Args()[0], call.Args()[1], false)
	case operators.LogicalNot:
		return p.pushNot(state, call.Args()[0])
	default:
		return false, ""
	}
}

func (p *Planner) pushComparison(state *constraintState, op string, lhs, rhs celast.Expr) (bool, string) {
	if key, value, ok := labelEqualityOperands(lhs, rhs); ok {
		switch op {
		case operators.Equals:
			return addLabelRequirement(state, key, selection.Equals, []string{value})
		case operators.NotEquals:
			return addLabelRequirement(state, key, selection.NotEquals, []string{value})
		}
	}

	path, literal, ok := pathLiteralOperands(lhs, rhs)
	if !ok {
		return false, ""
	}

	switch path {
	case "apiVersion":
		if op != operators.Equals {
			return false, ""
		}
		if state.apiVersion != "" && state.apiVersion != literal {
			return false, "conflicting apiVersion constraint left in residual"
		}
		state.apiVersion = literal
		return true, ""
	case "kind":
		if op != operators.Equals {
			return false, ""
		}
		if state.kind != "" && state.kind != literal {
			return false, "conflicting kind constraint left in residual"
		}
		state.kind = literal
		return true, ""
	case "metadata.namespace":
		if op != operators.Equals {
			return p.pushFieldSelector(state, op, path, literal)
		}
		if state.namespace != "" && state.namespace != literal {
			return false, "conflicting namespace constraint left in residual"
		}
		state.namespace = literal
		return true, ""
	case "metadata.name":
		if op == operators.Equals {
			if state.name != "" && state.name != literal {
				return false, "conflicting name constraint left in residual"
			}
			state.name = literal
		}
		return p.pushFieldSelector(state, op, path, literal)
	default:
		return p.pushFieldSelector(state, op, path, literal)
	}
}

func (p *Planner) pushIn(state *constraintState, lhs, rhs celast.Expr, negated bool) (bool, string) {
	if key, values, ok := labelMembershipOperands(lhs, rhs); ok {
		if negated {
			return addLabelRequirement(state, key, selection.NotIn, values)
		}
		return addLabelRequirement(state, key, selection.In, values)
	}
	if key, ok := labelExistenceOperands(lhs, rhs); ok {
		if negated {
			return addLabelRequirement(state, key, selection.DoesNotExist, nil)
		}
		return addLabelRequirement(state, key, selection.Exists, nil)
	}
	return false, ""
}

func (p *Planner) pushNot(state *constraintState, arg celast.Expr) (bool, string) {
	if !isCall(arg, operators.In, operators.OldIn) {
		return false, ""
	}
	call := arg.AsCall()
	return p.pushIn(state, call.Args()[0], call.Args()[1], true)
}

func (p *Planner) pushFieldSelector(state *constraintState, op, path, literal string) (bool, string) {
	gvk := schema.GroupVersionKind{}
	if state.apiVersion != "" {
		if gv, err := schema.ParseGroupVersion(state.apiVersion); err == nil {
			gvk = gv.WithKind(state.kind)
		}
	}
	if !p.opts.FieldSupport.SupportsField(gvk, path) {
		return false, ""
	}
	switch op {
	case operators.Equals:
		state.fieldSelectors = append(state.fieldSelectors, fields.OneTermEqualSelector(path, literal))
	case operators.NotEquals:
		state.fieldSelectors = append(state.fieldSelectors, fields.OneTermNotEqualSelector(path, literal))
	default:
		return false, ""
	}
	return true, ""
}

func addLabelRequirement(state *constraintState, key string, op selection.Operator, values []string) (bool, string) {
	req, err := labels.NewRequirement(key, op, values)
	if err != nil {
		return false, err.Error()
	}
	state.labelRequirements = append(state.labelRequirements, *req)
	return true, ""
}

func labelEqualityOperands(lhs, rhs celast.Expr) (string, string, bool) {
	if key, ok := labelKeyPath(lhs); ok {
		if value, ok := stringLiteral(rhs); ok {
			return key, value, true
		}
	}
	if key, ok := labelKeyPath(rhs); ok {
		if value, ok := stringLiteral(lhs); ok {
			return key, value, true
		}
	}
	return "", "", false
}

func labelMembershipOperands(lhs, rhs celast.Expr) (string, []string, bool) {
	key, ok := labelKeyPath(lhs)
	if !ok {
		return "", nil, false
	}
	values, ok := stringListLiteral(rhs)
	if !ok {
		return "", nil, false
	}
	return key, values, true
}

func labelExistenceOperands(lhs, rhs celast.Expr) (string, bool) {
	key, ok := stringLiteral(lhs)
	if !ok {
		return "", false
	}
	path, ok := objectPath(rhs)
	return key, ok && path == "metadata.labels"
}

func labelKeyPath(expr celast.Expr) (string, bool) {
	if expr.Kind() != celast.CallKind {
		return "", false
	}
	call := expr.AsCall()
	if call.FunctionName() != operators.Index && call.FunctionName() != operators.OptIndex {
		return "", false
	}
	path, ok := objectPath(call.Args()[0])
	if !ok || path != "metadata.labels" {
		return "", false
	}
	return stringLiteral(call.Args()[1])
}

func pathLiteralOperands(lhs, rhs celast.Expr) (string, string, bool) {
	if path, ok := objectPath(lhs); ok {
		if literal, ok := scalarLiteral(rhs); ok {
			return path, literal, true
		}
	}
	if path, ok := objectPath(rhs); ok {
		if literal, ok := scalarLiteral(lhs); ok {
			return path, literal, true
		}
	}
	return "", "", false
}

func objectPath(expr celast.Expr) (string, bool) {
	switch expr.Kind() {
	case celast.IdentKind:
		return "", expr.AsIdent() == "object"
	case celast.SelectKind:
		selectExpr := expr.AsSelect()
		base, ok := objectPath(selectExpr.Operand())
		if !ok {
			return "", false
		}
		if base == "" {
			return selectExpr.FieldName(), true
		}
		return base + "." + selectExpr.FieldName(), true
	default:
		return "", false
	}
}

func scalarLiteral(expr celast.Expr) (string, bool) {
	if expr.Kind() != celast.LiteralKind {
		return "", false
	}
	switch value := expr.AsLiteral().Value().(type) {
	case string:
		return value, true
	case int64, uint64, float64, bool:
		return fmt.Sprint(value), true
	default:
		return "", false
	}
}

func stringLiteral(expr celast.Expr) (string, bool) {
	if expr.Kind() != celast.LiteralKind {
		return "", false
	}
	value, ok := expr.AsLiteral().Value().(string)
	return value, ok
}

func stringListLiteral(expr celast.Expr) ([]string, bool) {
	if expr.Kind() != celast.ListKind {
		return nil, false
	}
	values := make([]string, 0, expr.AsList().Size())
	for _, element := range expr.AsList().Elements() {
		value, ok := stringLiteral(element)
		if !ok {
			return nil, false
		}
		values = append(values, value)
	}
	return values, true
}

func isCall(expr celast.Expr, names ...string) bool {
	if expr.Kind() != celast.CallKind {
		return false
	}
	name := expr.AsCall().FunctionName()
	for _, candidate := range names {
		if name == candidate {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
