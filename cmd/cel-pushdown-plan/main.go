package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	pushdown "cel-pushdown"
	kubepushdown "cel-pushdown/backend/kubernetes"
)

type planOutput struct {
	Backend  string             `json:"backend"`
	Original string             `json:"original"`
	Exact    bool               `json:"exact"`
	Searches []searchOutput     `json:"searches"`
	Warnings []pushdown.Warning `json:"warnings,omitempty"`
}

type searchOutput struct {
	ID          string             `json:"id"`
	Native      kubepushdown.Query `json:"native"`
	Residual    string             `json:"residual,omitempty"`
	Exact       bool               `json:"exact"`
	Explanation []string           `json:"explanation,omitempty"`
}

func main() {
	var backend string
	var expr string
	var maxBranches int
	flag.StringVar(&backend, "backend", "kubernetes", "backend to plan for")
	flag.StringVar(&expr, "expr", "", "CEL expression to plan")
	flag.IntVar(&maxBranches, "max-branches", 32, "maximum DNF branch expansion")
	flag.Parse()

	if expr == "" {
		fmt.Fprintln(os.Stderr, "--expr is required")
		os.Exit(2)
	}
	if backend != "kubernetes" {
		fmt.Fprintf(os.Stderr, "unsupported backend %q\n", backend)
		os.Exit(2)
	}

	planner := kubepushdown.NewPlanner()
	plan, err := planner.Plan(context.Background(), expr, pushdown.WithMaxBranches(maxBranches))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	out := planOutput{
		Backend:  plan.Backend,
		Original: plan.Original,
		Exact:    plan.Exact,
		Warnings: plan.Warnings,
	}
	for _, search := range plan.Searches {
		residual := ""
		if search.Residual != nil {
			residual = search.Residual.Source
		}
		out.Searches = append(out.Searches, searchOutput{
			ID:          search.ID,
			Native:      search.Native,
			Residual:    residual,
			Exact:       search.Exact,
			Explanation: search.Explanation,
		})
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
