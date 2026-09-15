package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/watch"
)

// walk visits every command in the tree.
func walk(cmd *cobra.Command, fn func(*cobra.Command)) {
	fn(cmd)
	for _, sub := range cmd.Commands() {
		walk(sub, fn)
	}
}

func TestWatchIsWiredOntoEveryReadVerb(t *testing.T) {
	root := NewRootCommand("test")

	var watched, unwatched []string
	walk(root, func(c *cobra.Command) {
		hasFlag := c.Flags().Lookup(flagWatch) != nil
		annotated := c.Annotations[watchAnnotation] != ""
		if hasFlag != annotated {
			t.Errorf("%s: --watch flag=%v but annotation=%v", c.CommandPath(), hasFlag, annotated)
		}
		if !annotated {
			if c.Runnable() && watchableVerbs[c.Name()] && c.Annotations[groupAnnotation] == "" && !c.Hidden {
				unwatched = append(unwatched, c.CommandPath())
			}
			return
		}
		watched = append(watched, c.CommandPath())
		if !watchableVerbs[c.Name()] {
			t.Errorf("%s is watchable but is not a read verb", c.CommandPath())
		}
	})

	// The feature's whole point is that it lands on the read surface at once
	// rather than per command, so a wiring bug that reached only a handful
	// would still pass every behavioural test. This is the guard on that.
	if len(watched) < 200 {
		t.Errorf("only %d commands are watchable; the read surface is far larger", len(watched))
	}
	for _, path := range unwatched {
		if !watchDenied[path] {
			t.Errorf("%s is a read verb but was not wired for --watch", path)
		}
	}
}

func TestWatchDeniedCommandsHaveNoWatchFlag(t *testing.T) {
	root := NewRootCommand("test")
	seen := map[string]bool{}
	walk(root, func(c *cobra.Command) {
		if !watchDenied[c.CommandPath()] {
			return
		}
		seen[c.CommandPath()] = true
		if c.Flags().Lookup(flagWatch) != nil {
			t.Errorf("%s is denied but still carries --watch", c.CommandPath())
		}
	})
	// A denial that names a command path which no longer exists is a denial
	// that silently stopped applying.
	for path := range watchDenied {
		if !seen[path] {
			t.Errorf("watchDenied names %q, which is not a command in the tree", path)
		}
	}
}

func TestWatchDoesNotReachWriteVerbs(t *testing.T) {
	root := NewRootCommand("test")
	for _, path := range [][]string{
		{"server", "delete"},
		{"server", "create"},
		{"baremetal", "node", "deploy"},
		{"volume", "create"},
	} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("finding %v: %v", path, err)
		}
		if cmd.Flags().Lookup(flagWatch) != nil {
			t.Errorf("%s carries --watch", cmd.CommandPath())
		}
	}
}

// TestWatchAndWaitFlagSetsStayDisjoint guards the reason --watch is registered
// per command rather than as a root persistent flag: a persistent --watch would
// land on the fifteen write commands that carry --wait, adding seven more
// candidates to every --w… prefix an operator types there. Keeping the two sets
// disjoint means no write command's flag surface changed at all.
func TestWatchAndWaitFlagSetsStayDisjoint(t *testing.T) {
	root := NewRootCommand("test")
	var waiters int
	walk(root, func(c *cobra.Command) {
		if c.Flags().Lookup("wait") == nil {
			return
		}
		waiters++
		if c.Flags().Lookup(flagWatch) != nil {
			t.Errorf("%s has both --watch and --wait", c.CommandPath())
		}
	})
	if waiters == 0 {
		t.Fatal("no --wait command was found; the check is measuring nothing")
	}

	// And prefix expansion behaves on both sides of the split: unambiguous
	// --watch… prefixes resolve on a read verb, and a write verb's --w… prefixes
	// still resolve exactly as they did (`--wa` is ambiguous there between
	// --wait and --wait-timeout, and was before --watch existed).
	for _, tc := range []struct {
		path   []string
		prefix string
		want   string
	}{
		{[]string{"server", "list"}, "--watch-p", "--watch-plain"},
		{[]string{"server", "list"}, "--watch-u", "--watch-until-change"},
		{[]string{"server", "list"}, "--wat", "--wat"}, // seven --watch* flags
		{[]string{"server", "create"}, "--wa", "--wa"},
		{[]string{"server", "create"}, "--wait-t", "--wait-timeout"},
	} {
		args := append(append([]string{}, tc.path...), tc.prefix)
		got := ExpandFlagPrefixes(root, args)
		if last := got[len(got)-1]; last != tc.want {
			t.Errorf("%v %s expanded to %q, want %q", tc.path, tc.prefix, last, tc.want)
		}
	}
}

func TestWatchShorthandIsFree(t *testing.T) {
	root := NewRootCommand("test")
	walk(root, func(c *cobra.Command) {
		f := c.Flags().ShorthandLookup("w")
		if f != nil && f.Name != flagWatch {
			t.Errorf("%s: -w is taken by --%s", c.CommandPath(), f.Name)
		}
	})
}

// runRoot executes the assembled tree with args, returning stdout and the error.
func runRoot(t *testing.T, root *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

func TestWatchFlagValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "below the interval floor",
			args: []string{"server", "list", "--watch=50ms"},
			want: "the shortest refresh interval is 250ms",
		},
		{
			name: "unparseable interval",
			args: []string{"server", "list", "--watch=soon"},
			want: `invalid --watch "soon"`,
		},
		{
			name: "unknown error mode",
			args: []string{"server", "list", "--watch=1s", "--watch-errors=loud"},
			want: "must be tolerate or exit",
		},
		{
			name: "negative count",
			args: []string{"server", "list", "--watch=1s", "--watch-count=-1"},
			want: "must not be negative",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runRoot(t, NewRootCommand("test"), tc.args...)
			if err == nil {
				t.Fatal("the invocation was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestWatchDetachedDurationIsAnsweredWithTheFix(t *testing.T) {
	// `--watch 1s` is the form pflag cannot take: a flag with a NoOptDefVal
	// never consumes the next token, so the duration lands as a positional and
	// cobra says "accepts 0 arg(s), received 1".
	_, err := runRoot(t, NewRootCommand("test"), "server", "list", "--watch", "1s")
	if err == nil {
		t.Fatal("the invocation was accepted")
	}
	if !strings.Contains(err.Error(), "did you mean --watch=1s") {
		t.Errorf("error %q does not suggest the attached form", err)
	}
}

func TestWatchLeavesRealPositionalsAlone(t *testing.T) {
	// A `show` takes a reference, and a server may well be called "1s". The
	// hint must not fire where the argument validator is happy.
	root := NewRootCommand("test")
	cmd, _, err := root.Find([]string{"server", "show"})
	if err != nil {
		t.Fatalf("finding server show: %v", err)
	}
	if err := cmd.Args(cmd, []string{"1s"}); err != nil {
		t.Errorf("a single positional was rejected: %v", err)
	}
}

func TestWatchTitleOmitsGlobalFlags(t *testing.T) {
	root := NewRootCommand("test")
	cmd, _, err := root.Find([]string{"server", "list"})
	if err != nil {
		t.Fatalf("finding server list: %v", err)
	}
	// --os-password is where the credentials live, and a status line is exactly
	// the sort of thing that ends up in a screenshot.
	if err := cmd.ParseFlags([]string{
		"--all", "--host", "node-14", "--os-password", "hunter2", "-c", "Name", "--watch=1s",
	}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}

	title := watchTitle(cmd)
	if strings.Contains(title, "hunter2") || strings.Contains(title, "os-password") {
		t.Fatalf("the status line would carry a credential: %q", title)
	}
	for _, want := range []string{"koc server list", "--all", "--host node-14"} {
		if !strings.Contains(title, want) {
			t.Errorf("title %q is missing %q", title, want)
		}
	}
	if strings.Contains(title, "--watch") {
		t.Errorf("title %q repeats the interval the status line already reports", title)
	}
	if strings.Contains(title, "column") {
		t.Errorf("title %q carries a global flag", title)
	}
}

func TestWatchRefusesDebugAndTimingOnATerminal(t *testing.T) {
	for _, flag := range []string{"--debug", "--timing"} {
		// The writer here is a buffer, not a terminal, so the command is in
		// append mode and the combination is allowed; resolve is exercised
		// directly to test the repaint-mode refusal.
		wf := &watchFlags{}
		a := &auth.Options{Debug: flag == "--debug", Timing: flag == "--timing"}
		err := wf.rejectDumpingFlags(a, false)
		if err == nil {
			t.Fatalf("%s with --watch was accepted", flag)
		}
		if !strings.Contains(err.Error(), flag) || !strings.Contains(err.Error(), flagWatchPlain) {
			t.Errorf("error %q should name %s and point at --%s", err, flag, flagWatchPlain)
		}
		// Appending frames overwrites nothing, so there it is fine.
		if err := wf.rejectDumpingFlags(a, true); err != nil {
			t.Errorf("%s was refused in append mode too: %v", flag, err)
		}
	}
}

func TestWatchResolvesDiffFromTheEnvironment(t *testing.T) {
	root := NewRootCommand("test")
	cmd, _, err := root.Find([]string{"server", "list"})
	if err != nil {
		t.Fatalf("finding server list: %v", err)
	}
	wf := &watchFlags{fs: cmd.Flags()}

	t.Setenv("NO_COLOR", "")
	if !wf.resolveDiff(false) {
		t.Error("highlighting should default on when frames are repainted")
	}
	if wf.resolveDiff(true) {
		t.Error("highlighting must be off when frames are appended: no escapes at all")
	}
	t.Setenv("NO_COLOR", "1")
	if wf.resolveDiff(false) {
		t.Error("NO_COLOR was ignored")
	}
}

// TestWatchAuthenticatesOnce is the per-command cost check the whole feature
// rests on: a watched list must mint one Keystone token however many times it
// refreshes. `watch -n1 koc server list` mints one a second, forever.
func TestWatchAuthenticatesOnce(t *testing.T) {
	var tokens, lists atomic.Int64

	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("POST /v3/auth/tokens", func(w http.ResponseWriter, _ *http.Request) {
		tokens.Add(1)
		w.Header().Set("X-Subject-Token", "tok")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"token":{"methods":["password"],` + //nolint:errcheck // httptest
			`"expires_at":"2099-01-01T00:00:00.000000Z",` +
			`"project":{"id":"p1","name":"proj","domain":{"id":"default","name":"Default"}},` +
			`"user":{"id":"u1","name":"alice","domain":{"id":"default","name":"Default"}},` +
			`"roles":[{"id":"r1","name":"admin"}],` +
			`"catalog":[{"type":"compute","name":"nova","id":"c1","endpoints":[` +
			`{"id":"e1","interface":"public","region":"RegionOne","region_id":"RegionOne",` +
			`"url":"` + base + `/v2.1"}]}]}}`))
	})
	mux.HandleFunc("GET /v2.1/servers/detail", func(w http.ResponseWriter, _ *http.Request) {
		lists.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"servers":[{"id":"11111111-1111-1111-1111-111111111111",` + //nolint:errcheck // httptest
			`"name":"web-01","status":"ACTIVE","addresses":{},"flavor":{"original_name":"m1.small"}}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	t.Setenv("OS_AUTH_URL", srv.URL+"/v3")
	t.Setenv("OS_USERNAME", "alice")
	t.Setenv("OS_PASSWORD", "pw")
	t.Setenv("OS_PROJECT_NAME", "proj")
	t.Setenv("OS_USER_DOMAIN_NAME", "Default")
	t.Setenv("OS_PROJECT_DOMAIN_NAME", "Default")
	t.Setenv("OS_CLOUD", "")
	t.Setenv("NO_COLOR", "1")

	out, err := runRoot(t, NewRootCommand("test"),
		"server", "list", "-c", "Name", "-c", "Status",
		"--watch="+watch.MinInterval.String(), "--watch-count=3")
	if err != nil {
		t.Fatalf("server list --watch: %v\n%s", err, out)
	}
	if got := lists.Load(); got != 3 {
		t.Errorf("issued %d list requests, want 3", got)
	}
	if got := tokens.Load(); got != 1 {
		t.Errorf("issued %d token requests, want exactly 1 — the restart tax is the whole point", got)
	}
	if n := strings.Count(out, "web-01"); n != 3 {
		t.Errorf("rendered %d frames, want 3:\n%s", n, out)
	}
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("appended output carries an escape sequence:\n%q", out)
	}
}
