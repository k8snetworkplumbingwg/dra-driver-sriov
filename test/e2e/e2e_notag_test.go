//go:build !e2e

package e2e_test

import "testing"

// Ensures go test ./... succeeds when -tags=e2e is not set.
func TestE2ERequiresBuildTag(t *testing.T) {
	t.Skip("workload e2e requires: go test -tags=e2e ./test/e2e/")
}
