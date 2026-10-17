package shell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

// The REPL is driven through x/term's Terminal on an in-memory io.ReadWriter,
// so no test needs a PTY: what a test "types" is the bytes a terminal would
// send — "\r" for Enter, "\t" for Tab, 0x03 for Ctrl-C, 0x04 for Ctrl-D.

// fakeKoc is a stand-in for cli.Run: it records every invocation and runs
// whatever a test registered for the first word.
type fakeKoc struct {
	mu    sync.Mutex
	calls [][]string
	// behave maps a command word to what it does; nil means "succeed".
	behave map[string]func(ctx context.Context, args []string, out io.Writer) error
}

type failure struct{ code int }

func (f failure) Error() string { return "failed" }

func (k *fakeKoc) config() Config {
	return Config{
		Run: func(ctx context.Context, args []string, prepare func(*cobra.Command, []string)) (*cobra.Command, error) {
			root := testTree()
			var out bytes.Buffer
			root.SetOut(&out)
			if prepare != nil {
				prepare(root, args)
			}
			k.mu.Lock()
			k.calls = append(k.calls, args)
			fn := k.behave[args[0]]
			k.mu.Unlock()
			cmd := root
			if c, _, err := root.Find(args); err == nil {
				cmd = c
			}
			if fn == nil {
				_, _ = io.WriteString(root.OutOrStdout(), "ran "+strings.Join(args, " ")+"\n")
				return cmd, nil
			}
			return cmd, fn(ctx, args, root.OutOrStdout())
		},
		ExitStatus: func(_ *cobra.Command, err error) (int, string) {
			var f failure
			switch {
			case err == nil:
				return 0, ""
			case errors.Is(err, context.Canceled):
				return 130, "koc: interrupted"
			case errors.As(err, &f):
				return f.code, "koc: failed"
			}
			return 1, "koc: " + err.Error()
		},
		NewRoot: testTree,
		Expand:  func(_ *cobra.Command, a []string) []string { return a },
	}
}

func (k *fakeKoc) ran() [][]string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([][]string(nil), k.calls...)
}

// testTree is a small koc-shaped command tree for completion.
func testTree() *cobra.Command {
	root := &cobra.Command{Use: "koc", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().String("os-password", "", "password")
	root.PersistentFlags().String("os-region-name", "", "region")
	server := &cobra.Command{Use: "server", Short: "Compute server commands"}
	list := &cobra.Command{Use: "list", Short: "List servers", RunE: func(*cobra.Command, []string) error { return nil }}
	list.Flags().Bool("long", false, "more columns")
	show := &cobra.Command{
		Use: "show", Short: "Show a server", RunE: func(*cobra.Command, []string) error { return nil },
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return []string{"web-01\tACTIVE", "web-02\tSHUTOFF", "db-01"}, cobra.ShellCompDirectiveNoFileComp
		},
	}
	server.AddCommand(list, show)
	root.AddCommand(server)
	return root
}

// session runs a shell over typed input and returns everything it wrote.
type session struct {
	k      *fakeKoc
	sh     *Shell
	out    bytes.Buffer // the terminal: prompts, echo, command output
	errOut bytes.Buffer
}

// pipeRW is the in-memory terminal: input from in, output to out.
type pipeRW struct {
	io.Reader
	io.Writer
}

func newSession(t *testing.T, k *fakeKoc, input io.Reader) *session {
	t.Helper()
	t.Setenv(EnvHistoryFile, "")
	s := &session{k: k}
	con := newTermConsole(pipeRW{input, &s.out}, -1)
	s.sh = newShell(k.config(), con, &s.out, &s.errOut)
	return s
}

func (s *session) run(t *testing.T) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.sh.loop(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("loop: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the shell did not end")
	}
}

func TestShell_RunsLinesAndSurvivesErrors(t *testing.T) {
	k := &fakeKoc{behave: map[string]func(context.Context, []string, io.Writer) error{
		"boom": func(context.Context, []string, io.Writer) error { return failure{code: 3} },
	}}
	s := newSession(t, k, strings.NewReader("boom\rkoc server list\ropenstack server show x\r\x04"))
	s.run(t)

	want := [][]string{{"boom"}, {"server", "list"}, {"server", "show", "x"}}
	if got := k.ran(); !reflect.DeepEqual(got, want) {
		t.Errorf("ran %q, want %q (a failing line must not end the shell; a koc/openstack prefix is dropped)", got, want)
	}
	if !strings.Contains(s.out.String(), "koc 3 $ ") {
		t.Errorf("the prompt after a failure does not show its status:\n%q", s.out.String())
	}
	if !strings.Contains(s.errOut.String(), "koc: failed") {
		t.Errorf("the failure was not reported: %q", s.errOut.String())
	}
	if s.sh.status != 0 {
		t.Errorf("status after a successful last line = %d, want 0", s.sh.status)
	}
}

func TestShell_RefusesShellSyntax(t *testing.T) {
	k := &fakeKoc{}
	s := newSession(t, k, strings.NewReader("server list | grep web\rserver list > out\rserver show $X\rserver list\r\x04"))
	s.run(t)
	if got, want := k.ran(), [][]string{{"server", "list"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("ran %q, want only the plain line %q", got, want)
	}
	for _, msg := range []string{"pipes (|)", "redirects (>)", "variables ($)"} {
		if !strings.Contains(s.errOut.String(), msg) {
			t.Errorf("no plain refusal naming %s:\n%s", msg, s.errOut.String())
		}
	}
}

// blockingInput feeds a line, then waits for release before the next.
type blockingInput struct {
	chunks  []string
	release chan struct{}
	i       int
}

func (b *blockingInput) Read(p []byte) (int, error) {
	if b.i >= len(b.chunks) {
		return 0, io.EOF
	}
	if b.i > 0 {
		<-b.release
	}
	n := copy(p, b.chunks[b.i])
	b.i++
	return n, nil
}

// TestShell_CtrlCCancelsOnlyTheCommand: SIGINT while a command runs cancels
// that command's context; the shell reports 130 and reads the next line.
func TestShell_CtrlCCancelsOnlyTheCommand(t *testing.T) {
	started := make(chan struct{})
	k := &fakeKoc{behave: map[string]func(context.Context, []string, io.Writer) error{
		"slow": func(ctx context.Context, _ []string, _ io.Writer) error {
			close(started)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return errors.New("never cancelled")
			}
		},
	}}
	in := &blockingInput{chunks: []string{"slow\r", "server list\r", "exit\r"}, release: make(chan struct{})}
	s := newSession(t, k, in)
	sigs := make(chan os.Signal, 1)
	s.sh.interrupts = sigs
	go func() {
		<-started
		sigs <- syscall.SIGINT
		close(in.release)
	}()
	s.run(t)

	if got, want := k.ran(), [][]string{{"slow"}, {"server", "list"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ran %q, want %q", got, want)
	}
	if !strings.Contains(s.out.String(), "koc 130 $ ") {
		t.Errorf("the prompt after Ctrl-C does not show 130:\n%q", s.out.String())
	}
}

func TestShell_SigtermEndsAfterTheLine(t *testing.T) {
	k := &fakeKoc{}
	s := newSession(t, k, strings.NewReader("server list\rserver show x\r"))
	sigs := make(chan os.Signal, 1)
	sigs <- syscall.SIGTERM
	s.sh.interrupts = sigs
	s.run(t)
	if got := k.ran(); len(got) > 1 {
		t.Errorf("ran %q after SIGTERM, want the shell to end", got)
	}
}

func TestShell_CtrlCAtThePromptAbandonsTheLine(t *testing.T) {
	k := &fakeKoc{}
	s := newSession(t, k, strings.NewReader("server delete everything\x03server list\r\x04"))
	s.run(t)
	if got, want := k.ran(), [][]string{{"server", "list"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("ran %q, want %q: Ctrl-C at the prompt must clear the line, not end the shell or run it", got, want)
	}
}

func TestShell_Builtins(t *testing.T) {
	t.Setenv("OS_AUTH_URL", "https://keystone.example.com:5000/v3")
	t.Setenv("OS_REGION_NAME", "RegionOne")
	t.Setenv("OS_PROJECT_NAME", "demo")
	t.Setenv("OS_USERNAME", "alice")
	t.Setenv("OS_TOKEN", "gAAAAAsecret-token")
	k := &fakeKoc{}
	s := newSession(t, k, strings.NewReader("scope\rhelp server\rclear\rshell\rexit\rserver list\r"))
	s.run(t)

	out := s.out.String()
	for _, want := range []string{"keystone.example.com:5000", "RegionOne", "demo", "alice", "koc [RegionOne · demo] "} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret-token") {
		t.Error("scope printed the token")
	}
	if !strings.Contains(out, "\x1b[2J") {
		t.Error("clear did not clear the screen")
	}
	if !strings.Contains(s.errOut.String(), "already in koc shell") {
		t.Errorf("a nested shell was not refused: %q", s.errOut.String())
	}
	// help is koc's own; exit ends the shell before the last line.
	if got, want := k.ran(), [][]string{{"help", "server"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("ran %q, want %q", got, want)
	}
}

func TestShell_Completion(t *testing.T) {
	k := &fakeKoc{}
	// "ser<Tab>" → "server ", "li<Tab>" → "list ", "--lo<Tab>" → "--long ";
	// "web<Tab>" → "web-0" (db-01 does not match), the second Tab lists.
	s := newSession(t, k, strings.NewReader("ser\tli\t--lo\t\rserver show web\t\t1\r\x04"))
	s.run(t)

	want := [][]string{{"server", "list", "--long"}, {"server", "show", "web-01"}}
	if got := k.ran(); !reflect.DeepEqual(got, want) {
		t.Errorf("ran %q, want %q", got, want)
	}
	// Two candidates share "web-0": the second Tab lists them, with their
	// descriptions, above the prompt.
	if out := s.out.String(); !strings.Contains(out, "web-01  ACTIVE") || !strings.Contains(out, "web-02  SHUTOFF") {
		t.Errorf("the candidates were not listed:\n%q", out)
	}
}

func TestShell_HistoryFileRedactsSecrets(t *testing.T) {
	k := &fakeKoc{}
	home := t.TempDir()
	t.Setenv("KOC_HOME", home)
	s := newSession(t, k, strings.NewReader("server list --os-password hunter2\rserver list\r\x1b[A\x1b[A\r\x04"))
	t.Setenv(EnvHistoryFile, "history")
	hist, err := openHistory(func(l string) string { return redactLine(testTree, l) })
	if err != nil {
		t.Fatal(err)
	}
	s.sh.hist = hist
	s.sh.con.(*termConsole).t.History = hist
	s.run(t)

	// Up, Up recalls the in-memory line with the password, as typed.
	if got := k.ran(); len(got) != 3 || !reflect.DeepEqual(got[2], got[0]) {
		t.Errorf("ran %q; Up Up should recall the first line", got)
	}
	raw, err := os.ReadFile(filepath.Join(home, "history"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Errorf("the history file holds the password:\n%s", raw)
	}
	if !strings.Contains(string(raw), "server list --os-password ***") {
		t.Errorf("history file:\n%s", raw)
	}

	// A new session starts with the persisted lines.
	again, err := openHistory(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again.close()
	// Three lines: the recalled one was run again.
	if again.Len() != 3 || again.At(0) != "server list --os-password ***" || again.At(1) != "server list" {
		t.Errorf("reloaded %d entries, most recent %q", again.Len(), again.At(0))
	}
}

func TestPipedShell(t *testing.T) {
	k := &fakeKoc{}
	var out, errOut bytes.Buffer
	err := Run(context.Background(), k.config(), strings.NewReader("server list\n# a comment\n\nserver show 'web 01'\n"), &out, &errOut, [][2]string{{"os-region-name", "R2"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := k.ran(), [][]string{{"server", "list"}, {"server", "show", "web 01"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("ran %q, want %q", got, want)
	}
	if strings.Contains(out.String(), "$ ") {
		t.Errorf("a piped shell printed prompts:\n%s", out.String())
	}
}

func TestSplitLine(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
		err  string
	}{
		{in: `server list`, want: []string{"server", "list"}},
		{in: `  server   show  web  `, want: []string{"server", "show", "web"}},
		{in: `server set --name 'my server' x`, want: []string{"server", "set", "--name", "my server", "x"}},
		{in: `server set --description "it's \"quoted\"" x`, want: []string{"server", "set", "--description", `it's "quoted"`, "x"}},
		{in: `a\ b c\\d`, want: []string{"a b", `c\d`}},
		{in: `x '' y`, want: []string{"x", "", "y"}},
		{in: `x 'a|b' "c;d" e\|f`, want: []string{"x", "a|b", "c;d", "e|f"}},
		{in: `x "a\nb"`, want: []string{"x", `a\nb`}},
		{in: `server list # the rest is a comment`, want: []string{"server", "list"}},
		{in: `x a#b`, want: []string{"x", "a#b"}},
		{in: `x --property=a=b`, want: []string{"x", "--property=a=b"}},
		{in: `x 'open`, err: "unterminated"},
		{in: `x \`, err: "unterminated"},
		{in: `a | b`, err: "pipes"},
		{in: `a || b`, err: "command lists (||)"},
		{in: `a && b`, err: "command lists (&&)"},
		{in: `a & `, err: "background jobs"},
		{in: `a; b`, err: "command lists (;)"},
		{in: `a > f`, err: "redirects (>)"},
		{in: `a < f`, err: "redirects (<)"},
		{in: `a $HOME`, err: "variables"},
		{in: `a "$HOME"`, err: "variables"},
		{in: `a $(id)`, err: "command substitution"},
		{in: "a `id`", err: "command substitution"},
		{in: `a *`, err: "globbing"},
		{in: `a ?`, err: "globbing"},
		{in: `(a)`, err: "subshells"},
	} {
		got, err := splitLine(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("splitLine(%q) = %q, %v; want an error naming %q", tc.in, got, err, tc.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitLine(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestQuoteArgRoundTrips(t *testing.T) {
	for _, s := range []string{"plain", "with space", "it's", `a"b`, "a|b", "", "$x", "#c"} {
		got, err := splitLine("x " + quoteArg(s))
		if err != nil || len(got) != 2 || got[1] != s {
			t.Errorf("quoteArg(%q) = %q reads back as %q, %v", s, quoteArg(s), got, err)
		}
	}
}

func TestFormatPrompt(t *testing.T) {
	long := promptScope{
		region:  "a-very-long-region-name-that-goes-on",
		project: "an-even-longer-project-name-for-the-payments-team",
	}
	for _, tc := range []struct {
		s      promptScope
		status int
		want   string
	}{
		{promptScope{}, 0, "koc $ "},
		{promptScope{}, 2, "koc 2 $ "},
		{promptScope{region: "RegionOne", project: "demo"}, 0, "koc [RegionOne · demo] $ "},
		{promptScope{project: "1a2b3c4d"}, 130, "koc [1a2b3c4d] 130 $ "},
	} {
		if got := formatPrompt(tc.s, tc.status, 80); got != tc.want {
			t.Errorf("formatPrompt(%+v, %d) = %q, want %q", tc.s, tc.status, got, tc.want)
		}
	}
	for _, width := range []int{80, 40, 200} {
		p := formatPrompt(long, 130, width)
		limit := max(width/promptMaxShare, promptMinWidth)
		if n := utf8.RuneCountInString(p); n > limit {
			t.Errorf("width %d: prompt %q is %d columns, over %d", width, p, n, limit)
		}
		if !strings.HasPrefix(p, "koc [a-ver") || !strings.Contains(p, "· a") || !strings.HasSuffix(p, "130 $ ") {
			t.Errorf("width %d: prompt %q lost its shape", width, p)
		}
	}
	if p := formatPrompt(long, 0, 80); !strings.Contains(p, "…") {
		t.Errorf("a truncated prompt %q does not show the cut", p)
	}
}

func TestRedactArgs(t *testing.T) {
	root := testTree()
	root.PersistentFlags().Bool("root-password", false, "prompt")
	for _, tc := range []struct{ in, want []string }{
		{[]string{"server", "list", "--os-password", "pw", "--long"}, []string{"server", "list", "--os-password", "***", "--long"}},
		{[]string{"server", "list", "--os-password=pw"}, []string{"server", "list", "--os-password=***"}},
		{[]string{"server", "list", "--root-password", "x"}, []string{"server", "list", "--root-password", "x"}},
		{[]string{"server", "list", "--os-region-name", "--os-password"}, []string{"server", "list", "--os-region-name", "--os-password"}},
		{[]string{"x", "--unknown-token", "t"}, []string{"x", "--unknown-token", "***"}},
	} {
		if got := redactArgs(root, tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("redactArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
