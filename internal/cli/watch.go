package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/watch"
)

// watchAnnotation marks a command the watch layer has wrapped. It is what lets
// the argument-validation wrapper below tell a --watch invocation from any
// other, and it gives anything walking the tree a way to ask whether a command
// refreshes.
const watchAnnotation = "koc_watch"

// Flag names. They are registered on the *watched command itself*, never as
// root persistent flags, and that placement is load-bearing.
//
// --wait and --wait-timeout already exist on fifteen write commands (`server
// create`, `node deploy`, `volume create`, …). A root-persistent --watch would
// put seven more --w… flags on every one of them, and ExpandFlagPrefixes
// deliberately leaves an ambiguous prefix alone for cobra to reject — so a
// prefix that resolves on those commands today would start failing tomorrow
// for a flag they cannot even use. Registering --watch only on the read verbs
// keeps the two sets disjoint: no command has both, and no write command's
// flag surface changed at all.
const (
	flagWatch            = "watch"
	flagWatchDiff        = "watch-diff"
	flagWatchCount       = "watch-count"
	flagWatchErrors      = "watch-errors"
	flagWatchUntilChange = "watch-until-change"
	flagWatchPlain       = "watch-plain"
	flagWatchNoTitle     = "watch-no-title"
)

// ErrWatchStopped reports a refresh loop the operator stopped with Ctrl-C.
//
// It wraps context.Canceled, so every existing check — cmd/koc's exit-130 path
// included — still matches it. cmd/koc recognises the sentinel itself only to
// leave off the "the server-side operation may still be running" caveat that
// belongs to an interrupted `node deploy --wait`: a watched read verb has
// nothing outstanding server-side, and Ctrl-C is how a watch is *meant* to end,
// so the caveat would be on screen every single time and wrong every single
// time.
var ErrWatchStopped = fmt.Errorf("watch interrupted: %w", context.Canceled)

// watchableVerbs are the leaf names the watch layer wraps. Both are read-only
// by construction across the whole tree, which is what makes a name-based rule
// safe here: there is no `koc <noun> list` that writes anything.
var watchableVerbs = map[string]bool{"list": true, "show": true}

// watchDenied names the read verbs that must not refresh, by command path.
//
// The verb name is a good rule but not a perfect one, and these are the places
// it is wrong. Every other list/show leaf in the tree is a plain GET.
//
//   - `console url show` is a "show" that POSTs: it calls nova's
//     remote-consoles API, which *mints* a console session and an auth token
//     per call. Refreshing it once a second would leave nova holding thousands
//     of them.
//   - `server password show` reads the private key's passphrase from the
//     terminal, unechoed, or the key itself from standard input. Neither
//     survives being re-run into a frame buffer, and a stored password does not
//     change while you look at it.
//
// Both spellings of the nested commands are listed: `console url show` is also
// reachable as `server console url show` (see aliases.go), and a denial that
// covers only one of them is not a denial.
var watchDenied = map[string]bool{
	"koc console url show":        true,
	"koc server console url show": true,
	"koc server password show":    true,
}

// enableWatch gives every read-only leaf in the tree the --watch flag family.
// It runs in the same final pass as requireSubcommands, once the whole tree is
// assembled, so a service that adds a noun gets the feature without knowing it
// exists — the alternative was editing 212 command files.
func enableWatch(root *cobra.Command, a *auth.Options, o *output.Options) {
	for _, sub := range root.Commands() {
		enableWatch(sub, a, o)
	}
	if !watchable(root) {
		return
	}
	newWatchFlags().attach(root, a, o)
}

// watchable reports whether cmd is a leaf the watch layer should wrap.
func watchable(cmd *cobra.Command) bool {
	if !cmd.Runnable() || cmd.RunE == nil || cmd.Hidden {
		return false
	}
	if cmd.Annotations[groupAnnotation] != "" {
		return false
	}
	if !watchableVerbs[cmd.Name()] {
		return false
	}
	return !watchDenied[cmd.CommandPath()]
}

// watchFlags is one watched command's flag state.
type watchFlags struct {
	fs *pflag.FlagSet

	interval    string
	diff        bool
	count       int
	errors      string
	untilChange bool
	plain       bool
	noTitle     bool
}

func newWatchFlags() *watchFlags { return &watchFlags{} }

// attach registers the flags on cmd and wraps its RunE with the refresh loop.
func (wf *watchFlags) attach(cmd *cobra.Command, a *auth.Options, o *output.Options) {
	wf.register(cmd)
	wf.wrapArgs(cmd)
	wf.wrapRunE(cmd, a, o)

	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[watchAnnotation] = "true"
}

func (wf *watchFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	wf.fs = fl

	// One flag, not two. pflag's NoOptDefVal is what makes a bare --watch and
	// an explicit --watch=1s the same flag: without a value it takes the
	// default, with one it takes that. -w is unused as a shorthand anywhere in
	// the tree.
	fl.StringVarP(&wf.interval, flagWatch, "w", "",
		"refresh this command in place every DURATION (default "+watch.DefaultInterval.String()+
			"); the interval must be attached with '=', as --watch=1s")
	fl.Lookup(flagWatch).NoOptDefVal = watch.DefaultInterval.String()

	// Registered false so cobra does not advertise "(default true)": the real
	// default is neither — it is on when the output is a terminal and NO_COLOR
	// is unset, which only resolveDiff can decide. What matters to pflag is
	// that the flag was Changed.
	fl.BoolVar(&wf.diff, flagWatchDiff, false,
		"highlight what changed since the previous refresh (default: on when the output is a terminal)")
	fl.IntVar(&wf.count, flagWatchCount, 0,
		"stop after this many refreshes; 0 refreshes until interrupted")
	fl.StringVar(&wf.errors, flagWatchErrors, watch.ErrorsTolerate,
		"what a failed refresh does: tolerate (keep the last good frame) or exit")
	fl.BoolVar(&wf.untilChange, flagWatchUntilChange, false,
		"exit 0 as soon as a refresh differs from the one before it")
	fl.BoolVar(&wf.plain, flagWatchPlain, false,
		"append whole frames to the stream instead of repainting one in place, and emit no escape sequences")
	fl.BoolVar(&wf.noTitle, flagWatchNoTitle, false,
		"suppress the status line")
}

// wrapRunE turns the command's own RunE into the loop's renderer. Nothing in
// the command changes: it writes to cmd.OutOrStdout() as it always has, which
// is now the frame buffer.
func (wf *watchFlags) wrapRunE(cmd *cobra.Command, a *auth.Options, o *output.Options) {
	orig := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if !wf.enabled() {
			return orig(c, args)
		}
		wo, err := wf.resolve(c, a, o)
		if err != nil {
			return err
		}

		// A watched command re-resolves every --project/--user/--image name it
		// was given on every tick. Turning the resolver memo on is what stops
		// that from being a Keystone lookup a second for an answer that does
		// not change.
		resolve.Enable()

		out, errOut := c.OutOrStdout(), c.ErrOrStderr()
		differ := watch.NewDiffer(wo.Diff)
		wo.Differ = differ
		o.SetHighlighter(differ)
		defer func() {
			// The command's writers and the output layer's per-frame state are
			// process-wide, so they are handed back even on the error path —
			// cmd/koc prints to stderr after this returns.
			c.SetOut(out)
			c.SetErr(errOut)
			o.SetHighlighter(nil)
			o.SetDisplayWidth(0)
		}()

		render := func(ctx context.Context, frame, warn io.Writer) error {
			// Measured per frame rather than once, so a resize is picked up
			// even where SIGWINCH does not exist.
			width, _ := watch.Size(out)
			o.SetDisplayWidth(width)
			c.SetOut(frame)
			c.SetErr(warn)
			c.SetContext(ctx)
			return orig(c, args)
		}
		if err := watch.Run(c.Context(), wo, out, errOut, render); err != nil {
			if errors.Is(err, context.Canceled) {
				return ErrWatchStopped
			}
			return err
		}
		return nil
	}
}

func (wf *watchFlags) enabled() bool { return wf.fs != nil && wf.fs.Changed(flagWatch) }

// resolve turns the flags into a validated watch.Options.
func (wf *watchFlags) resolve(cmd *cobra.Command, a *auth.Options, o *output.Options) (watch.Options, error) {
	interval, err := wf.resolveInterval()
	if err != nil {
		return watch.Options{}, err
	}
	if wf.errors != watch.ErrorsTolerate && wf.errors != watch.ErrorsExit {
		return watch.Options{}, fmt.Errorf("invalid --%s %q: must be %s or %s",
			flagWatchErrors, wf.errors, watch.ErrorsTolerate, watch.ErrorsExit)
	}
	if wf.count < 0 {
		return watch.Options{}, fmt.Errorf("invalid --%s %d: must not be negative", flagWatchCount, wf.count)
	}

	out := cmd.OutOrStdout()
	plain := wf.plain || !watch.IsTerminal(out)
	if err := wf.rejectDumpingFlags(a, plain); err != nil {
		return watch.Options{}, err
	}

	return watch.Options{
		Interval:      interval,
		Diff:          wf.resolveDiff(plain),
		Count:         wf.count,
		ErrorMode:     wf.errors,
		UntilChange:   wf.untilChange,
		Plain:         plain,
		NoTitle:       wf.noTitle,
		Keys:          !plain && watch.StdinIsTerminal(),
		Title:         watchTitle(cmd),
		CSVHeaderOnce: o.Format == output.FormatCSV,
	}, nil
}

func (wf *watchFlags) resolveInterval() (time.Duration, error) {
	d, err := time.ParseDuration(wf.interval)
	if err != nil {
		return 0, fmt.Errorf("invalid --%s %q: %w (expected a duration such as 1s, 500ms or 2m)",
			flagWatch, wf.interval, err)
	}
	if d < watch.MinInterval {
		// A koc tick is an authenticated API call against a shared control
		// plane, not a local command, so the floor is a guard on the cloud
		// rather than on the terminal.
		return 0, fmt.Errorf("invalid --%s %s: the shortest refresh interval is %s",
			flagWatch, d, watch.MinInterval)
	}
	return d, nil
}

// resolveDiff decides whether to highlight changes. It is on by default on a
// terminal and off elsewhere, and NO_COLOR turns it off — matching how
// `hypervisor list --gauge` already resolves colour. An explicit --watch-diff
// outranks all of that, in both directions.
func (wf *watchFlags) resolveDiff(plain bool) bool {
	if wf.fs.Changed(flagWatchDiff) {
		return wf.diff && !plain
	}
	return !plain && os.Getenv("NO_COLOR") == ""
}

// rejectDumpingFlags refuses the two global flags that write to the terminal
// behind the output layer's back.
//
// --debug dumps a whole request and response per tick, which would bury the
// frame it is meant to sit beside; --timing's totals are reported once, at
// exit, and mean nothing summed over an unbounded loop (the status line reports
// per-tick latency instead, which is the number that matters here). Both are
// fine in --watch-plain, where frames are appended rather than repainted and
// nothing is overwritten — so the error says so rather than just refusing.
func (wf *watchFlags) rejectDumpingFlags(a *auth.Options, plain bool) error {
	if plain {
		return nil
	}
	var named []string
	if a.Debug {
		named = append(named, "--debug")
	}
	if a.Timing {
		named = append(named, "--timing")
	}
	if len(named) == 0 {
		return nil
	}
	return fmt.Errorf("%s cannot be combined with --%s: the per-request output would overwrite the frame"+
		"\nadd --%s to append frames instead of repainting one",
		strings.Join(named, " and "), flagWatch, flagWatchPlain)
}

// watchTitle is the status line's left-hand side: the command being refreshed
// and the filters that make one watch different from the next.
//
// Only the command's *local* flags are included. That is not a shortcut — the
// global flags are where the credentials live (--os-password, --vault-token,
// --os-application-credential-secret), and a status line is exactly the sort of
// thing that ends up in a screenshot. The local flags on a read verb are its
// filters, which is what the operator needs to tell two watches apart.
func watchTitle(cmd *cobra.Command) string {
	// cmd.Flags() is the *complete* set, inherited persistent flags included, so
	// the global ones are excluded by name rather than by hoping a cobra
	// accessor draws the line in the right place.
	inherited := map[string]bool{}
	cmd.InheritedFlags().VisitAll(func(f *pflag.Flag) { inherited[f.Name] = true })

	parts := []string{cmd.CommandPath()}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if inherited[f.Name] || strings.HasPrefix(f.Name, flagWatch) {
			return // global, or already reported as the interval
		}
		if f.Value.Type() == "bool" {
			parts = append(parts, "--"+f.Name)
			return
		}
		parts = append(parts, "--"+f.Name+" "+f.Value.String())
	})
	return strings.Join(parts, " ")
}

// wrapArgs answers `--watch 1s` — the form pflag cannot take, because a flag
// with a NoOptDefVal never consumes the next token — with the fix rather than
// with cobra's "accepts 0 arg(s), received 1".
//
// It only speaks when the command's own argument validation has already
// rejected the line, which is what keeps it from guessing: `koc server show
// --watch 1s` leaves `1s` as the server reference, where it may well be one,
// and that invocation is not touched.
func (wf *watchFlags) wrapArgs(cmd *cobra.Command) {
	prev := cmd.Args
	if prev == nil {
		return
	}
	cmd.Args = func(c *cobra.Command, args []string) error {
		err := prev(c, args)
		if err == nil || !wf.enabled() || len(args) == 0 {
			return err
		}
		last := args[len(args)-1]
		if _, perr := time.ParseDuration(last); perr != nil {
			return err
		}
		return fmt.Errorf("%w\n\ndid you mean --%s=%s? the interval attaches with '=', "+
			"so that --%s on its own still means every %s", err, flagWatch, last, flagWatch, watch.DefaultInterval)
	}
}
