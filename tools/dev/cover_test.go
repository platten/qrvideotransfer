package main

import (
	"slices"
	"testing"
)

// TestCoverTimeout: the coverage step sets its own per-package limit of 15 minutes instead of go
// test's unchosen 10-minute default (designer's ruling on PR #47; DESIGN.md section 19); the job
// and step budgets stay the real guard.
func TestCoverTimeout(t *testing.T) {
	a := coverArgs("noasm,nounsafe")
	if !slices.Contains(a, "-timeout=15m") || !slices.Contains(a, "-coverprofile=PROFILE") || a[len(a)-1] != "./..." {
		t.Errorf("coverage arguments %v", a)
	}
}
