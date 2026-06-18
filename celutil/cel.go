package celutil

import (
	"context"
	"fmt"

	pushdown "cel-pushdown"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
)

// NewEnv builds the CEL environment used by planners and residual evaluation.
func NewEnv(opts pushdown.PlanOptions) (*cel.Env, error) {
	envOpts := []cel.EnvOption{
		cel.Variable("object", cel.DynType),
	}
	envOpts = append(envOpts, opts.EnvOptions...)
	return cel.NewEnv(envOpts...)
}

// CompilePredicate parses, checks, and prepares a CEL predicate for evaluation.
func CompilePredicate(source string, opts pushdown.PlanOptions) (*pushdown.CELPredicate, error) {
	env, err := NewEnv(opts)
	if err != nil {
		return nil, err
	}
	ast, issues := env.Compile(source)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	program, err := env.Program(ast)
	if err != nil {
		return nil, err
	}
	return &pushdown.CELPredicate{Source: source, Ast: ast, Program: program}, nil
}

// EvalPredicate evaluates a compiled predicate with the supplied activation.
func EvalPredicate(ctx context.Context, pred *pushdown.CELPredicate, activation map[string]any) (bool, error) {
	if pred == nil {
		return true, nil
	}
	out, _, err := pred.Program.ContextEval(ctx, activation)
	if err != nil {
		return false, err
	}
	if out == types.True {
		return true, nil
	}
	if out == types.False {
		return false, nil
	}
	if b, ok := out.Value().(bool); ok {
		return b, nil
	}
	return false, fmt.Errorf("residual CEL did not evaluate to bool: %v", out.Type().TypeName())
}
