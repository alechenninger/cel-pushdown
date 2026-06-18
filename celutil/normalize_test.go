package celutil_test

import (
	"testing"

	pushdown "cel-pushdown"
	"cel-pushdown/celutil"

	"github.com/google/cel-go/cel"
)

func boolVars() pushdown.PlanOption {
	return pushdown.WithCELEnvOptions(
		cel.Variable("A", cel.BoolType),
		cel.Variable("B", cel.BoolType),
		cel.Variable("C", cel.BoolType),
		cel.Variable("D", cel.BoolType),
		cel.Variable("E", cel.BoolType),
	)
}

func normalizeBranches(t *testing.T, expr string, opts ...pushdown.PlanOption) []pushdown.Branch {
	t.Helper()
	planOpts := pushdown.ApplyPlanOptions(append([]pushdown.PlanOption{boolVars()}, opts...)...)
	normalized, err := celutil.Normalize(expr, planOpts)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	return normalized.Branches
}

func TestNormalizeAND(t *testing.T) {
	branches := normalizeBranches(t, "A && B && C")
	if len(branches) != 1 {
		t.Fatalf("branches = %d, want 1", len(branches))
	}
	if got := len(branches[0].Conjuncts); got != 3 {
		t.Fatalf("conjuncts = %d, want 3", got)
	}
}

func TestNormalizeOR(t *testing.T) {
	branches := normalizeBranches(t, "A || B")
	if len(branches) != 2 {
		t.Fatalf("branches = %d, want 2", len(branches))
	}
}

func TestNormalizeNestedOR(t *testing.T) {
	branches := normalizeBranches(t, "A && (B || C)")
	if len(branches) != 2 {
		t.Fatalf("branches = %d, want 2", len(branches))
	}
	for _, branch := range branches {
		if len(branch.Conjuncts) != 2 {
			t.Fatalf("conjuncts = %d, want 2", len(branch.Conjuncts))
		}
	}
}

func TestNormalizeCrossProductOR(t *testing.T) {
	branches := normalizeBranches(t, "(A || B) && (C || D)")
	if len(branches) != 4 {
		t.Fatalf("branches = %d, want 4", len(branches))
	}
}

func TestNormalizeBranchCap(t *testing.T) {
	opts := pushdown.ApplyPlanOptions(boolVars(), pushdown.WithMaxBranches(3))
	_, err := celutil.Normalize("(A || B) && (C || D)", opts)
	if err == nil {
		t.Fatal("Normalize() error = nil, want branch cap error")
	}
}
