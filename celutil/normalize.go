package celutil

import (
	"fmt"
	"strings"

	pushdown "cel-pushdown"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
)

// Normalized is the checked expression plus the DNF branches derived from it.
type Normalized struct {
	Predicate *pushdown.CELPredicate
	Branches  []pushdown.Branch
}

// Normalize parses, checks, and expands ordinary AND/OR expressions into DNF branches.
func Normalize(source string, opts pushdown.PlanOptions) (*Normalized, error) {
	pred, err := CompilePredicate(source, opts)
	if err != nil {
		return nil, err
	}
	branches, err := Branches(pred.Ast, opts.MaxBranches)
	if err != nil {
		return nil, err
	}
	return &Normalized{Predicate: pred, Branches: branches}, nil
}

// Branches expands a checked AST into branches of conjunctive predicates.
func Branches(ast *cel.Ast, maxBranches int) ([]pushdown.Branch, error) {
	if maxBranches <= 0 {
		maxBranches = 32
	}
	raw, err := dnf(ast.NativeRep().Expr(), maxBranches)
	if err != nil {
		return nil, err
	}
	branches := make([]pushdown.Branch, 0, len(raw))
	for _, conjuncts := range raw {
		nodes := make([]pushdown.ExprNode, 0, len(conjuncts))
		parts := make([]string, 0, len(conjuncts))
		for _, expr := range conjuncts {
			src := ExprSource(ast, expr)
			nodes = append(nodes, pushdown.ExprNode{Source: src, Expr: expr})
			parts = append(parts, src)
		}
		branches = append(branches, pushdown.Branch{
			Source:    strings.Join(parts, " && "),
			Conjuncts: nodes,
		})
	}
	return branches, nil
}

// ExprSource returns a stable CEL string for an AST expression.
func ExprSource(ast *cel.Ast, expr celast.Expr) string {
	src, err := cel.ExprToString(expr, ast.NativeRep().SourceInfo())
	if err != nil {
		return fmt.Sprintf("<expr:%d>", expr.ID())
	}
	return src
}

func dnf(expr celast.Expr, max int) ([][]celast.Expr, error) {
	if expr.Kind() != celast.CallKind {
		return [][]celast.Expr{{expr}}, nil
	}
	call := expr.AsCall()
	args := call.Args()
	switch call.FunctionName() {
	case operators.LogicalAnd:
		if len(args) != 2 {
			return [][]celast.Expr{{expr}}, nil
		}
		left, err := dnf(args[0], max)
		if err != nil {
			return nil, err
		}
		right, err := dnf(args[1], max)
		if err != nil {
			return nil, err
		}
		if len(left)*len(right) > max {
			return nil, fmt.Errorf("CEL branch expansion exceeds MaxBranches=%d", max)
		}
		out := make([][]celast.Expr, 0, len(left)*len(right))
		for _, l := range left {
			for _, r := range right {
				branch := make([]celast.Expr, 0, len(l)+len(r))
				branch = append(branch, l...)
				branch = append(branch, r...)
				out = append(out, branch)
			}
		}
		return out, nil
	case operators.LogicalOr:
		if len(args) != 2 {
			return [][]celast.Expr{{expr}}, nil
		}
		left, err := dnf(args[0], max)
		if err != nil {
			return nil, err
		}
		right, err := dnf(args[1], max)
		if err != nil {
			return nil, err
		}
		if len(left)+len(right) > max {
			return nil, fmt.Errorf("CEL branch expansion exceeds MaxBranches=%d", max)
		}
		return append(left, right...), nil
	default:
		return [][]celast.Expr{{expr}}, nil
	}
}
