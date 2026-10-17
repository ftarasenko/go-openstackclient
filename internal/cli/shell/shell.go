// Package shell is `koc shell`: an interactive REPL that runs koc commands and
// nothing else. It exists for an embedding host — a web console that attaches
// a browser terminal to a sandbox holding only koc — but works the same in any
// terminal.
//
// Every line runs on a freshly built command tree (see cli.Run), so a flag on
// one line never leaks into the next; a failing line sets the status shown in
// the prompt instead of ending the shell; Ctrl-C cancels the running command
// and returns to the prompt. Line editing, history and completion come from
// golang.org/x/term and cobra's own completion engine.
package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
)

// Config is what the shell needs from the cli package, passed in as functions
// because cli builds the command tree the shell is itself part of.
type Config struct {
	// Run executes one koc invocation on a fresh tree; prepare sees the tree
	// and the expanded args before execution. It is cli.Run.
	Run func(ctx context.Context, args []string, prepare func(root *cobra.Command, args []string)) (*cobra.Command, error)
	// ExitStatus maps Run's outcome to an exit status and a message. It is
	// cli.ExitStatus.
	ExitStatus func(cmd *cobra.Command, err error) (int, string)
	// NewRoot builds a fresh command tree, for completion and redaction.
	NewRoot func() *cobra.Command
	// Expand applies koc's command and flag abbreviation to args.
	Expand func(root *cobra.Command, args []string) []string
}

// NewCommand returns the `shell` command.
func NewCommand(cfg Config) *cobra.Command {
	return &cobra.Command{
		Use:   "shell",
		Short: "Run koc commands interactively, with completion and history (koc-specific)",
		Long: `Run koc commands interactively.

Type a koc command without the leading "koc" (a pasted "koc " or "openstack "
prefix is accepted). Arguments are split like a POSIX shell's — single and
double quotes and backslash escapes — but there are no pipes, redirects,
globbing, variables or command lists. Tab completes commands, flags and,
where koc can, values; Up and Down walk the history.

Built-ins: help [command], scope, clear, exit. Ctrl-C cancels the running
command, Ctrl-D or "exit" leaves.

Global flags given to "koc shell" itself apply to every line. See
docs/shell.md for KOC_SCOPE_FILE, KOC_HISTORY_FILE, KOC_AUDIT_FD and the
embedded profile.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return Run(cmd.Context(), cfg, os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr(), outerFlags(cmd))
		},
	}
}

// outerFlags are the global flags given to `koc shell` itself, as name=value,
// to apply to every line as though typed on it.
func outerFlags(cmd *cobra.Command) [][2]string {
	var out [][2]string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if cmd.LocalNonPersistentFlags().Lookup(f.Name) == nil {
			out = append(out, [2]string{f.Name, f.Value.String()})
		}
	})
	return out
}

// Shell is one interactive session.
type Shell struct {
	cfg    Config
	con    console
	out    io.Writer
	errOut io.Writer
	hist   *history
	// outer holds the global flags given to `koc shell` itself.
	outer [][2]string
	// status is the exit status of the last line, shown in the prompt.
	status int
	// width is the terminal width as last measured.
	width int
	// interrupts delivers SIGINT while a command runs; nil in tests.
	interrupts <-chan os.Signal
	// envScope is the scope the OS_* environment describes, read once.
	envScope scopeInfo
	// sigterm records a SIGTERM; the shell ends once the current line is done.
	sigterm atomic.Bool
}

// Run runs the shell until exit, Ctrl-D, end of input or SIGTERM.
// A terminal on in gets the line editor; anything else is read as plain lines.
func Run(ctx context.Context, cfg Config, in io.Reader, out, errOut io.Writer, outer [][2]string) error {
	// main's signal context is cancelled by the first SIGINT, and every Ctrl-C
	// in the shell is meant for the running command, not the shell. Detach from
	// it and take both signals here: SIGINT cancels the current line, SIGTERM
	// ends the shell once that line is done.
	ctx = context.WithoutCancel(ctx)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	var con console = newPlainConsole(in)
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		con = newTermConsole(struct {
			io.Reader
			io.Writer
		}{in, out}, int(f.Fd()))
	}
	s := newShell(cfg, con, out, errOut)
	s.outer = outer
	s.interrupts = sigs
	return s.loop(ctx)
}

func newShell(cfg Config, con console, out, errOut io.Writer) *Shell {
	s := &Shell{cfg: cfg, con: con, out: out, errOut: errOut, envScope: envScope()}
	hist, err := openHistory(func(l string) string { return redactLine(cfg.NewRoot, l) })
	if err != nil {
		s.warnf("history: %v; keeping it in memory only", err)
	}
	s.hist = hist
	if tc, ok := con.(*termConsole); ok {
		tc.t.History = hist
		tc.t.AutoCompleteCallback = s.autoComplete
	}
	return s
}

// loop reads and runs lines until the shell ends.
func (s *Shell) loop(ctx context.Context) error {
	defer s.hist.close()
	for {
		s.width = s.con.width()
		line, err := s.con.readLine(s.prompt())
		if errors.Is(err, io.EOF) {
			if _, ok := s.con.(*termConsole); ok {
				// Leave the terminal's own prompt on a fresh line.
				_, _ = io.WriteString(s.out, "\n")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if s.runLine(ctx, line) {
			return nil
		}
		if s.terminated() {
			return nil
		}
	}
}

// terminated reports whether a SIGTERM arrived (SIGINTs between lines are
// dropped: they can only have been meant for a command that already ended).
func (s *Shell) terminated() bool {
	for {
		select {
		case sig := <-s.interrupts:
			if sig == syscall.SIGTERM {
				s.sigterm.Store(true)
			}
		default:
			return s.sigterm.Load()
		}
	}
}

// runLine runs one line and reports whether the shell should end.
func (s *Shell) runLine(ctx context.Context, line string) (exit bool) {
	args, err := splitLine(line)
	if err != nil {
		s.status = 2
		s.errorf("%v", err)
		return false
	}
	args = stripPrefix(args)
	if len(args) == 0 {
		return false
	}
	if done, exit := s.builtin(ctx, args); done {
		return exit
	}
	s.status = s.exec(ctx, args)
	return false
}

// stripPrefix drops a pasted leading "koc" or "openstack".
func stripPrefix(args []string) []string {
	if len(args) > 0 && (args[0] == "koc" || args[0] == "openstack") {
		return args[1:]
	}
	return args
}

// builtin runs the shell's own commands. done is false for a koc command.
func (s *Shell) builtin(ctx context.Context, args []string) (done, exit bool) {
	switch args[0] {
	case "exit", "quit":
		return true, true
	case "clear":
		_, _ = io.WriteString(s.out, "\x1b[H\x1b[2J")
		s.status = 0
		return true, false
	case "scope":
		s.printScope()
		s.status = 0
		return true, false
	case "help":
		s.status = s.exec(ctx, args)
		return true, false
	case "shell":
		s.status = 2
		s.errorf("already in koc shell")
		return true, false
	}
	return false, false
}

// exec runs one koc command, with Ctrl-C cancelling only it.
func (s *Shell) exec(ctx context.Context, args []string) int {
	lineCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case sig := <-s.interrupts:
			if sig == syscall.SIGTERM {
				s.sigterm.Store(true)
			}
			cancel()
		case <-done:
		}
	}()

	cmd, err := s.cfg.Run(lineCtx, args, s.prepare)
	code, msg := s.cfg.ExitStatus(cmd, err)
	if msg != "" {
		s.println(msg)
	}
	return code
}

// prepare runs on each line's fresh tree before it executes.
func (s *Shell) prepare(root *cobra.Command, _ []string) {
	root.SetOut(s.out)
	root.SetErr(s.errOut)
	for _, kv := range s.outer {
		_ = root.PersistentFlags().Set(kv[0], kv[1])
	}
}

func (s *Shell) prompt() string {
	return formatPrompt(s.scope().prompt(), s.status, s.width)
}

// scope is the scope the next line runs in.
func (s *Shell) scope() scopeInfo { return s.envScope }

// scopeInfo is what the shell knows of the scope commands run in.
type scopeInfo struct {
	authURL     string
	region      string
	projectID   string
	projectName string
	userName    string
	cloud       string
	expiresAt   time.Time
}

func (si scopeInfo) prompt() promptScope {
	p := promptScope{region: si.region, project: si.projectName}
	if p.project == "" {
		p.project = shortID(si.projectID)
	}
	if p.region == "" && p.project == "" {
		p.project = si.cloud
	}
	return p
}

// envScope reads the scope from the OS_* environment, once, at start.
func envScope() scopeInfo {
	return scopeInfo{
		authURL:     os.Getenv("OS_AUTH_URL"),
		region:      os.Getenv("OS_REGION_NAME"),
		projectID:   firstEnv("OS_PROJECT_ID", "OS_TENANT_ID"),
		projectName: firstEnv("OS_PROJECT_NAME", "OS_TENANT_NAME"),
		userName:    firstEnv("OS_USERNAME", "OS_USER_ID"),
		cloud:       os.Getenv("OS_CLOUD"),
	}
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// printScope is the `scope` built-in. It never prints the token.
func (s *Shell) printScope() {
	si := s.scope()
	host := si.authURL
	if u, err := url.Parse(si.authURL); err == nil && u.Host != "" {
		host = u.Host
	}
	project := si.projectName
	switch {
	case project != "" && si.projectID != "":
		project += " (" + si.projectID + ")"
	case project == "":
		project = si.projectID
	}
	expiry := "unknown"
	if !si.expiresAt.IsZero() {
		left := time.Until(si.expiresAt).Round(time.Minute)
		if left > 0 {
			expiry = fmt.Sprintf("%s (in %s)", si.expiresAt.UTC().Format(time.RFC3339), left)
		} else {
			expiry = si.expiresAt.UTC().Format(time.RFC3339) + " (expired)"
		}
	}
	rows := [][2]string{
		{"auth host", host},
		{"cloud", si.cloud},
		{"region", si.region},
		{"project", project},
		{"user", si.userName},
		{"token expires", expiry},
	}
	var b strings.Builder
	for _, r := range rows {
		if r[1] == "" {
			if r[0] == "cloud" {
				continue
			}
			r[1] = "-"
		}
		fmt.Fprintf(&b, "%-14s %s\n", r[0]+":", r[1])
	}
	_, _ = io.WriteString(s.out, b.String())
}

func (s *Shell) writeAbovePrompt(text string) { s.con.writeAbovePrompt(text) }

func (s *Shell) println(msg string) { _, _ = fmt.Fprintln(s.errOut, msg) }

func (s *Shell) errorf(format string, a ...any) {
	s.println("koc shell: " + fmt.Sprintf(format, a...))
}

func (s *Shell) warnf(format string, a ...any) {
	s.println("koc shell: warning: " + fmt.Sprintf(format, a...))
}
