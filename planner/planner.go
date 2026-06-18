package planner

import (
	"context"
	"fmt"

	pushdown "cel-pushdown"
	"cel-pushdown/celutil"
)

// BranchPlanner is implemented by backends that can push down one CEL branch.
type BranchPlanner[Q any] interface {
	BackendName() string
	PlanBranch(ctx context.Context, branch pushdown.Branch, opts pushdown.PlanOptions) ([]pushdown.Search[Q], error)
}

// Plan normalizes a CEL expression and asks the backend to plan each branch.
func Plan[Q any](ctx context.Context, backend BranchPlanner[Q], expr string, optFns ...pushdown.PlanOption) (*pushdown.Plan[Q], error) {
	opts := pushdown.ApplyPlanOptions(optFns...)
	normalized, err := celutil.Normalize(expr, opts)
	if err != nil {
		return nil, err
	}
	plan := &pushdown.Plan[Q]{
		Backend:  backend.BackendName(),
		Original: expr,
		Exact:    true,
	}
	nextID := 1
	for _, branch := range normalized.Branches {
		searches, err := backend.PlanBranch(ctx, branch, opts)
		if err != nil {
			return nil, err
		}
		for _, search := range searches {
			if search.ID == "" {
				search.ID = fmt.Sprintf("s%d", nextID)
			}
			nextID++
			if !search.Exact {
				plan.Exact = false
			}
			plan.Searches = append(plan.Searches, search)
		}
	}
	if len(plan.Searches) == 0 {
		plan.Exact = true
	}
	return plan, nil
}
