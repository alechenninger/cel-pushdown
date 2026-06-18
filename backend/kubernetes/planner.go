package kubernetes

import (
	"context"
	"fmt"
	"strings"

	pushdown "cel-pushdown"
	"cel-pushdown/celutil"
	genericplanner "cel-pushdown/planner"

	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// Planner plans CEL predicates into Kubernetes queries.
type Planner struct {
	Mapper             meta.RESTMapper
	FieldSupport       FieldSupport
	ResourceScope      ResourceScope
	AllowAllNamespaces bool
}

type PlannerOption func(*Planner)

func NewPlanner(opts ...PlannerOption) *Planner {
	p := &Planner{
		FieldSupport: BuiltinFieldSupport{},
	}
	for _, opt := range opts {
		opt(p)
	}
	if p.ResourceScope == nil && p.Mapper != nil {
		p.ResourceScope = restMapperScope{mapper: p.Mapper}
	}
	if p.FieldSupport == nil {
		p.FieldSupport = BuiltinFieldSupport{}
	}
	return p
}

func WithRESTMapper(mapper meta.RESTMapper) PlannerOption {
	return func(p *Planner) {
		p.Mapper = mapper
		if p.ResourceScope == nil && mapper != nil {
			p.ResourceScope = restMapperScope{mapper: mapper}
		}
	}
}

func WithFieldSupport(support FieldSupport) PlannerOption {
	return func(p *Planner) {
		p.FieldSupport = support
	}
}

func WithResourceScope(scope ResourceScope) PlannerOption {
	return func(p *Planner) {
		p.ResourceScope = scope
	}
}

func WithPlannerAllNamespaces(allow bool) PlannerOption {
	return func(p *Planner) {
		p.AllowAllNamespaces = allow
	}
}

func (p *Planner) BackendName() string { return BackendName }

func (p *Planner) Plan(ctx context.Context, expr string, opts ...pushdown.PlanOption) (*pushdown.Plan[Query], error) {
	return genericplanner.Plan[Query](ctx, p, expr, opts...)
}

type branchState struct {
	query          Query
	labelReqs      []labels.Requirement
	fieldSelectors []fields.Selector
	pushed         []bool
	explanation    []string
}

func newBranchState(conjuncts int) branchState {
	return branchState{
		query:  newQuery(),
		pushed: make([]bool, conjuncts),
	}
}

func (p *Planner) PlanBranch(ctx context.Context, branch pushdown.Branch, opts pushdown.PlanOptions) ([]pushdown.Search[Query], error) {
	states := []branchState{newBranchState(len(branch.Conjuncts))}
	var err error

	for i, node := range branch.Conjuncts {
		states, err = p.tryPushGVK(states, i, node, opts.MaxBranches)
		if err != nil {
			return nil, err
		}
		if len(states) == 0 {
			return nil, nil
		}
	}
	for i := range states {
		p.resolveResource(&states[i].query)
	}
	for i, node := range branch.Conjuncts {
		if allPushed(states, i) {
			continue
		}
		states, err = p.tryPushNonGVK(states, i, node, opts.MaxBranches)
		if err != nil {
			return nil, err
		}
		if len(states) == 0 {
			return nil, nil
		}
	}

	searches := make([]pushdown.Search[Query], 0, len(states))
	for _, state := range states {
		finalizeSelectors(&state.query, state.labelReqs, state.fieldSelectors)
		residual, err := residualPredicate(branch, state.pushed, opts)
		if err != nil {
			return nil, err
		}
		searches = append(searches, pushdown.Search[Query]{
			Native:      state.query,
			Residual:    residual,
			Exact:       residual == nil,
			Explanation: state.explanation,
		})
	}
	return searches, nil
}

func allPushed(states []branchState, idx int) bool {
	for _, state := range states {
		if !state.pushed[idx] {
			return false
		}
	}
	return len(states) > 0
}

func (p *Planner) tryPushGVK(states []branchState, idx int, node pushdown.ExprNode, maxBranches int) ([]branchState, error) {
	if vals, ok := matchPathStringSet(node.Expr, "apiVersion"); ok {
		return expandStates(states, idx, vals, maxBranches, func(state *branchState, val string) bool {
			group, version, ok := parseAPIVersion(val)
			if !ok {
				return false
			}
			if state.query.GVK.Group != "" && state.query.GVK.Group != group {
				return false
			}
			if state.query.GVK.Version != "" && state.query.GVK.Version != version {
				return false
			}
			state.query.GVK.Group = group
			state.query.GVK.Version = version
			state.explanation = append(state.explanation, "pushed apiVersion == "+val)
			return true
		})
	}
	if vals, ok := matchPathStringSet(node.Expr, "kind"); ok {
		return expandStates(states, idx, vals, maxBranches, func(state *branchState, val string) bool {
			if state.query.GVK.Kind != "" && state.query.GVK.Kind != val {
				return false
			}
			state.query.GVK.Kind = val
			state.explanation = append(state.explanation, "pushed kind == "+val)
			return true
		})
	}
	return states, nil
}

func (p *Planner) tryPushNonGVK(states []branchState, idx int, node pushdown.ExprNode, maxBranches int) ([]branchState, error) {
	if vals, ok := matchPathStringSet(node.Expr, "metadata", "namespace"); ok {
		return expandStates(states, idx, vals, maxBranches, func(state *branchState, val string) bool {
			if state.query.Namespace != "" && state.query.Namespace != val {
				return false
			}
			state.query.Namespace = val
			state.explanation = append(state.explanation, "pushed metadata.namespace == "+val)
			return true
		})
	}
	if vals, ok := matchPathStringSet(node.Expr, "metadata", "name"); ok {
		return expandStates(states, idx, vals, maxBranches, func(state *branchState, val string) bool {
			if state.query.Name != "" && state.query.Name != val {
				return false
			}
			state.query.Name = val
			state.explanation = append(state.explanation, "pushed metadata.name == "+val)
			return true
		})
	}
	if reqs, ok, err := p.labelRequirements(node.Expr); err != nil {
		return nil, err
	} else if ok {
		out := copyStates(states)
		for i := range out {
			out[i].labelReqs = append(out[i].labelReqs, reqs...)
			out[i].pushed[idx] = true
			out[i].explanation = append(out[i].explanation, "pushed label selector "+labels.Requirements(reqs).String())
		}
		return out, nil
	}
	if selector, keepResidual, ok := p.fieldSelector(states, node.Expr); ok {
		out := copyStates(states)
		for i := range out {
			out[i].fieldSelectors = append(out[i].fieldSelectors, selector)
			if !keepResidual {
				out[i].pushed[idx] = true
			}
			out[i].explanation = append(out[i].explanation, "pushed field selector "+selector.String())
		}
		return out, nil
	}
	return states, nil
}

func expandStates(states []branchState, idx int, vals []string, maxBranches int, apply func(*branchState, string) bool) ([]branchState, error) {
	if len(vals) == 0 {
		return states, nil
	}
	if len(states)*len(vals) > maxBranches {
		return nil, fmt.Errorf("kubernetes branch expansion exceeds MaxBranches=%d", maxBranches)
	}
	out := make([]branchState, 0, len(states)*len(vals))
	for _, state := range states {
		for _, val := range vals {
			next := cloneState(state)
			if !apply(&next, val) {
				continue
			}
			next.pushed[idx] = true
			out = append(out, next)
		}
	}
	return out, nil
}

func cloneState(state branchState) branchState {
	next := state
	next.labelReqs = append([]labels.Requirement(nil), state.labelReqs...)
	next.fieldSelectors = append([]fields.Selector(nil), state.fieldSelectors...)
	next.pushed = append([]bool(nil), state.pushed...)
	next.explanation = append([]string(nil), state.explanation...)
	return next
}

func copyStates(states []branchState) []branchState {
	out := make([]branchState, len(states))
	for i, state := range states {
		out[i] = cloneState(state)
	}
	return out
}

func (p *Planner) resolveResource(q *Query) {
	if p.AllowAllNamespaces {
		q.AllNamespaces = true
	}
	if p.Mapper == nil || q.GVK.Kind == "" {
		q.ResourceKnown = false
		return
	}
	versions := []string{}
	if q.GVK.Version != "" {
		versions = append(versions, q.GVK.Version)
	}
	mapping, err := p.Mapper.RESTMapping(q.GVK.GroupKind(), versions...)
	if err != nil || mapping == nil {
		q.ResourceKnown = false
		return
	}
	q.GVK = mapping.GroupVersionKind
	q.GVR = mapping.Resource
	q.ResourceKnown = true
}

func residualPredicate(branch pushdown.Branch, pushed []bool, opts pushdown.PlanOptions) (*pushdown.CELPredicate, error) {
	parts := []string{}
	for i, node := range branch.Conjuncts {
		if i >= len(pushed) || !pushed[i] {
			parts = append(parts, node.Source)
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return celutil.CompilePredicate(strings.Join(parts, " && "), opts)
}

func (p *Planner) labelRequirements(expr celast.Expr) ([]labels.Requirement, bool, error) {
	if inner, ok := notArg(expr); ok {
		left, right, inOK := binaryCall(inner, operators.In)
		if !inOK {
			left, right, inOK = binaryCall(inner, operators.OldIn)
		}
		if inOK {
			if path, pathOK := exprPath(left); pathOK && path.isLabelKey() {
				vals, listOK := literalStringList(right)
				if !listOK {
					return nil, false, nil
				}
				reqs, err := labelValueRequirements(path.key, selection.NotIn, vals)
				return reqs, err == nil, err
			}
			if key, litOK := literalString(left); litOK {
				if path, pathOK := exprPath(right); pathOK && path.isLabelsMap() {
					req, err := labels.NewRequirement(key, selection.DoesNotExist, nil)
					if err != nil {
						return nil, false, err
					}
					return []labels.Requirement{*req}, true, nil
				}
			}
		}
		return nil, false, nil
	}

	if left, right, op, ok := comparisonOperator(expr); ok {
		if path, pathOK := exprPath(left); pathOK && path.isLabelKey() {
			val, litOK := literalString(right)
			if !litOK {
				return nil, false, nil
			}
			selectionOp := selection.Equals
			if op == operators.NotEquals {
				selectionOp = selection.NotEquals
			}
			reqs, err := labelValueRequirements(path.key, selectionOp, []string{val})
			return reqs, err == nil, err
		}
		if path, pathOK := exprPath(right); pathOK && path.isLabelKey() && op == operators.Equals {
			val, litOK := literalString(left)
			if !litOK {
				return nil, false, nil
			}
			req, err := labels.NewRequirement(path.key, selection.Equals, []string{val})
			if err != nil {
				return nil, false, err
			}
			return []labels.Requirement{*req}, true, nil
		}
	}

	left, right, ok := binaryCall(expr, operators.In)
	if !ok {
		left, right, ok = binaryCall(expr, operators.OldIn)
	}
	if !ok {
		return nil, false, nil
	}
	if path, pathOK := exprPath(left); pathOK && path.isLabelKey() {
		vals, listOK := literalStringList(right)
		if !listOK {
			return nil, false, nil
		}
		req, err := labels.NewRequirement(path.key, selection.In, vals)
		if err != nil {
			return nil, false, err
		}
		return []labels.Requirement{*req}, true, nil
	}
	if key, litOK := literalString(left); litOK {
		if path, pathOK := exprPath(right); pathOK && path.isLabelsMap() {
			req, err := labels.NewRequirement(key, selection.Exists, nil)
			if err != nil {
				return nil, false, err
			}
			return []labels.Requirement{*req}, true, nil
		}
	}
	return nil, false, nil
}

func labelValueRequirements(key string, op selection.Operator, vals []string) ([]labels.Requirement, error) {
	req, err := labels.NewRequirement(key, op, vals)
	if err != nil {
		return nil, err
	}
	if op != selection.NotEquals && op != selection.NotIn {
		return []labels.Requirement{*req}, nil
	}
	exists, err := labels.NewRequirement(key, selection.Exists, nil)
	if err != nil {
		return nil, err
	}
	return []labels.Requirement{*exists, *req}, nil
}

func (p *Planner) fieldSelector(states []branchState, expr celast.Expr) (fields.Selector, bool, bool) {
	left, right, op, ok := comparisonOperator(expr)
	if !ok {
		return nil, false, false
	}
	path, pathOK := exprPath(left)
	valExpr := right
	if !pathOK && op == operators.Equals {
		path, pathOK = exprPath(right)
		valExpr = left
	}
	if !pathOK || path.hasKey {
		return nil, false, false
	}
	fieldPath := path.fieldPath()
	if fieldPath == "metadata.name" || fieldPath == "metadata.namespace" {
		return nil, false, false
	}
	if !strings.HasPrefix(fieldPath, "spec.") && !strings.HasPrefix(fieldPath, "status.") {
		return nil, false, false
	}
	val, litOK := literalValueString(valExpr)
	if !litOK {
		return nil, false, false
	}
	for _, state := range states {
		if p.FieldSupport == nil || !p.FieldSupport.SupportsField(state.query.GVK, fieldPath) {
			return nil, false, false
		}
	}
	if op == operators.NotEquals {
		return fields.OneTermNotEqualSelector(fieldPath, val), true, true
	}
	return fields.OneTermEqualSelector(fieldPath, val), false, true
}
