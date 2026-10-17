package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runCaptured is Run with the command's stdout and stderr captured.
func runCaptured(t *testing.T, args ...string) (stdout string, code int) {
	t.Helper()
	var out bytes.Buffer
	cmd, err := Run(context.Background(), "test", args, func(root *cobra.Command, _ []string) {
		root.SetOut(&out)
		root.SetErr(&out)
	})
	code, msg := ExitStatus(cmd, err)
	if msg != "" {
		out.WriteString(msg + "\n")
	}
	return out.String(), code
}

// TestRun_NoFlagLeakBetweenRuns: cobra and pflag keep flag values on a command
// between Execute calls, which is why Run builds a fresh tree every time. A
// --long on one run must not widen the next one.
func TestRun_NoFlagLeakBetweenRuns(t *testing.T) {
	startFleet(t, &keystoneFleet{})

	plain, code := runCaptured(t, "server", "list")
	if code != 0 {
		t.Fatalf("server list: exit %d\n%s", code, plain)
	}
	long, code := runCaptured(t, "server", "list", "--long")
	if code != 0 {
		t.Fatalf("server list --long: exit %d\n%s", code, long)
	}
	if long == plain {
		t.Fatalf("--long rendered the same table as without it; the test proves nothing:\n%s", long)
	}
	again, code := runCaptured(t, "server", "li") // the abbreviation expands too
	if code != 0 {
		t.Fatalf("server li: exit %d\n%s", code, again)
	}
	if again != plain {
		t.Errorf("--long leaked into the next run:\nfirst:\n%s\nafter --long:\n%s", plain, again)
	}
}

// TestRun_TimingReportedOnce: the --timing summary belongs to the run that
// asked for it, not to every later one in the same process.
func TestRun_TimingReportedOnce(t *testing.T) {
	startFleet(t, &keystoneFleet{})

	first := captureStderr(t, func() { runCaptured(t, "server", "list", "--timing") })
	if !strings.Contains(first, "timing: total") {
		t.Fatalf("--timing printed no summary:\n%s", first)
	}
	second := captureStderr(t, func() { runCaptured(t, "server", "list") })
	if strings.Contains(second, "timing:") {
		t.Errorf("a run without --timing repeated the previous summary:\n%s", second)
	}
}

func TestExitStatus(t *testing.T) {
	ran := &cobra.Command{SilenceUsage: true}
	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
		err  error
		code int
	}{
		{"ok", ran, nil, ExitOK},
		{"failure", ran, errors.New("boom"), ExitFailure},
		{"usage", &cobra.Command{}, errors.New("unknown flag"), ExitUsage},
		{"unknown verb", ran, fmt.Errorf("x: %w", ErrUnknownCommand), ExitUsage},
		{"interrupt", ran, context.Canceled, ExitInterrupted},
	} {
		if code, _ := ExitStatus(tc.cmd, tc.err); code != tc.code {
			t.Errorf("%s: exit %d, want %d", tc.name, code, tc.code)
		}
	}
}

// captureStderr runs fn with os.Stderr redirected and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stderr = orig }()
	fn()
	_ = w.Close()
	return <-done
}
