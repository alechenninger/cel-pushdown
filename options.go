package pushdown

import "github.com/google/cel-go/cel"

const defaultMaxBranches = 32

// PlanOptions controls CEL planning behavior.
type PlanOptions struct {
	MaxBranches int
	EnvOptions  []cel.EnvOption
}

// PlanOption mutates PlanOptions.
type PlanOption func(*PlanOptions)

// DefaultPlanOptions returns conservative planning defaults.
func DefaultPlanOptions() PlanOptions {
	return PlanOptions{MaxBranches: defaultMaxBranches}
}

// ApplyPlanOptions folds options into defaults.
func ApplyPlanOptions(opts ...PlanOption) PlanOptions {
	po := DefaultPlanOptions()
	for _, opt := range opts {
		opt(&po)
	}
	if po.MaxBranches <= 0 {
		po.MaxBranches = defaultMaxBranches
	}
	return po
}

// WithMaxBranches sets the maximum number of DNF branches the planner may produce.
func WithMaxBranches(max int) PlanOption {
	return func(opts *PlanOptions) {
		opts.MaxBranches = max
	}
}

// WithCELEnvOptions adds cel-go environment options to the default environment.
func WithCELEnvOptions(envOpts ...cel.EnvOption) PlanOption {
	return func(opts *PlanOptions) {
		opts.EnvOptions = append(opts.EnvOptions, envOpts...)
	}
}
