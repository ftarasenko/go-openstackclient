package main

import (
	"os"
	"testing"
)

// TestMainVersion runs main end to end on the one invocation that exits 0
// without a cloud. It also gives this package a test file: under
// `go test -coverpkg=./...` (make cover) a package without one is reported via
// `go tool covdata`, which toolchains fetched by GOTOOLCHAIN do not ship, and
// the whole run then exits 1.
func TestMainVersion(t *testing.T) {
	saved := os.Args
	t.Cleanup(func() { os.Args = saved })
	os.Args = []string{"koc", "--version"}

	main()
}
