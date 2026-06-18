package pushdown

import (
	"context"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
)

// NativeQuery is implemented by backend query types that can describe themselves.
type NativeQuery interface {
	BackendName() string
	String() string
}

// Plan is a backend-specific query plan with a strongly typed native query.
type Plan[Q any] struct {
	Backend  string      `json:"backend"`
	Original string      `json:"original"`
	Searches []Search[Q] `json:"searches"`
	Exact    bool        `json:"exact"`
	Warnings []Warning   `json:"warnings,omitempty"`
}

// Search is one native backend query plus an optional residual CEL predicate.
type Search[Q any] struct {
	ID          string        `json:"id"`
	Native      Q             `json:"native"`
	Residual    *CELPredicate `json:"-"`
	Exact       bool          `json:"exact"`
	Explanation []string      `json:"explanation,omitempty"`
}

// Candidate is a value returned by a backend search.
type Candidate[V any] struct {
	SearchID string
	Value    V
}

// CELPredicate is a checked CEL expression and executable program.
type CELPredicate struct {
	Source  string
	Ast     *cel.Ast
	Program cel.Program
}

// Warning describes a non-fatal planning concern.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ExprNode wraps a CEL AST expression with a stable string representation.
type ExprNode struct {
	Source string
	Expr   celast.Expr
}

// Branch represents one conjunction of predicates.
type Branch struct {
	Source    string
	Conjuncts []ExprNode
}

// BackendPlanner plans a CEL expression for one backend.
type BackendPlanner[Q any] interface {
	BackendName() string
	Plan(ctx context.Context, expr string, opts ...PlanOption) (*Plan[Q], error)
}

// BackendExecutor executes a typed backend plan.
type BackendExecutor[Q any, V any] interface {
	BackendName() string
	Execute(ctx context.Context, plan *Plan[Q]) ([]Candidate[V], error)
	ExecuteSearch(ctx context.Context, search Search[Q]) ([]Candidate[V], error)
}
