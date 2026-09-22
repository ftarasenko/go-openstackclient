package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/services"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/paging"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The "compute host" drains: emptying one compute host of its servers.
//
// There is no bulk API behind any of them — nova has no host-level endpoint —
// so each is a client-side loop over a per-server action, which is also all
// python-novaclient's `nova host-evacuate-live`, `host-servers-migrate` and
// `host-evacuate` are (novaclient/v2/shell.py). They differ in the action they
// post and, more importantly, in what nova requires of the host:
//
//	koc                      action           source host   guest downtime
//	compute host drain       os-migrateLive   up            none
//	compute host drain --cold  migrate        up            a reboot
//	compute host evacuate    evacuate         DOWN          it already crashed
//
// The first two are one verb and a flag because they are the same operation —
// move a running server while its own host is healthy — differing only in
// whether the guest stays up. Evacuation is a separate verb because its
// precondition is the opposite one.
//
// koc departs from novaclient's loop in four ways, in all three verbs:
//
//   - Discovery is GET /servers?host=&all_tenants=1 — one call, an exact match
//     on the compute service host — rather than a *substring* match on
//     hypervisor_hostname via os-hypervisors, which is why novaclient's own
//     help warns to pass an FQDN or risk draining more hosts than you meant.
//   - A server nova cannot act on is reported as skipped up front instead of
//     being POSTed at and counted as a failure.
//   - --max-servers caps how many servers move; --parallel caps how many move
//     at once. novaclient's --max-servers says "simultaneously" but only breaks
//     its loop after N, which is neither.
//   - A failure makes the command exit non-zero. novaclient collects per-server
//     errors into a table and still exits 0, so a drain that moved nothing is
//     indistinguishable from one that worked.
//
// drainTickInterval is how often the terminal status line is repainted, so the
// elapsed time advances while nothing else is happening. The per-server wait
// polls on migratePollInterval, as "server migrate --wait" does.
const drainTickInterval = time.Second

// Per-server drain outcomes, as rendered in the Result column.
const (
	drainCompleted = "completed"
	drainAccepted  = "accepted"
	drainFailed    = "failed"
	drainSkipped   = "skipped"
	drainPlanned   = "planned"
	drainDeferred  = "deferred"
)

// hostDrainFlags are the options every "compute host" drain accepts. Each verb
// adds its own on top (a target host, block-migration knobs, confirmation).
type hostDrainFlags struct {
	maxServers  int
	parallel    int
	wait        bool
	waitTimeout time.Duration
	dryRun      bool

	// pinMicroversion mirrors serverListFlags: the discovery listing is lowered
	// to the least microversion that answers it unless the operator named one.
	pinMicroversion bool
}

func (f *hostDrainFlags) validate() error {
	if f.maxServers < 0 {
		return errors.New("--max-servers must not be negative")
	}
	if f.parallel < 1 {
		return errors.New("--parallel must be at least 1")
	}
	return nil
}

// registerHostDrainFlags installs the shared flags. moves names the action in
// the help text ("migrated", "evacuated") so each verb reads naturally.
func registerHostDrainFlags(cmd *cobra.Command, f *hostDrainFlags, moves string) {
	fl := cmd.Flags()
	fl.IntVar(&f.maxServers, "max-servers", 0,
		"move at most this many servers, leaving the rest on the host; 0 moves all of them")
	fl.IntVar(&f.parallel, "parallel", 1, "move this many servers at a time")
	fl.BoolVar(&f.wait, "wait", false,
		"wait for each server to be "+moves+" instead of returning once nova accepts the request")
	fl.DurationVar(&f.waitTimeout, flagWaitTimeout, migratePollTimeout, helpWaitTimeout+" (per server)")
	fl.BoolVar(&f.dryRun, "dry-run", false, "report which servers would be moved without moving them")
}

// newComputeHostCommand builds the "compute host" parent group.
func newComputeHostCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Compute host (hypervisor) fleet operations",
	}
	cmd.AddCommand(
		newComputeHostDrainCommand(a, o),
		newComputeHostEvacuateCommand(a, o),
	)
	return cmd
}

// drainMode is what distinguishes the three drains: which servers they may
// touch, what they post, and what nova requires before and after.
type drainMode struct {
	// name is how the operation is spelled in messages ("live migration").
	name string
	// pastTense completes "Host X: N <pastTense>" in the closing summary.
	pastTense string
	// eligible reports whether a server in this status can be acted on;
	// whyNot explains the refusal in the skipped row's Detail.
	eligible func(status string) bool
	whyNot   func(status string) string
	// precheck validates the host itself before anything is posted. It is what
	// turns "nova would reject all fifty of these" into one message.
	precheck func(ctx context.Context, client *gophercloud.ServiceClient, host string) error
	// action is the request body, identical for every server in one run.
	action map[string]any
	// finish runs once a server has left the host, for the step nova requires
	// afterwards — cold migration's confirm. It returns the Detail to record.
	// nil when the move is complete on its own.
	finish func(ctx context.Context, client *gophercloud.ServiceClient, id, status string) (string, error)
}

// drainResult is one server's outcome, in nova's listing order.
type drainResult struct {
	id     string
	name   string
	status string
	result string
	detail string
}

// drainPlan is the whole host's servers plus the indices of the ones that will
// actually move. Workers address results by index, so the table stays in
// listing order however many run at once and no lock is needed to fill it.
type drainPlan struct {
	results []drainResult
	migrate []int
}

// drainOutput is where a drain's two streams go: the result table to stdout, so
// a piped run stays parseable, and the running narration to stderr, so the
// operator still sees what is moving. They travel together because every step
// below writes to one or the other.
type drainOutput struct {
	table    io.Writer
	progress io.Writer
}

// runHostDrain is the engine behind all three verbs.
func runHostDrain(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	host string, f *hostDrainFlags, mode *drainMode, out drainOutput,
) error {
	if mode.precheck != nil {
		if err := mode.precheck(ctx, client, host); err != nil {
			return err
		}
	}
	list, err := hostServers(ctx, client, host, f.pinMicroversion)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return emptyHostResult(ctx, client, host, o, out)
	}
	plan := planDrain(list, f.maxServers, mode)

	if f.dryRun {
		if err := o.WriteList(out.table, drainTable(plan.results)); err != nil {
			return err
		}
		_, err := fmt.Fprintf(out.progress, "Dry run: %d of %d servers on host %s would be moved by %s\n",
			len(plan.migrate), len(plan.results), host, mode.name)
		return err
	}

	pr := newDrainProgress(out.progress, host, len(plan.migrate))
	stop := pr.start()
	runDrain(ctx, client, host, plan, f, mode, pr)
	stop()

	if err := o.WriteList(out.table, drainTable(plan.results)); err != nil {
		return err
	}
	return pr.summarize(plan, f, mode, host)
}

// hostServers lists every server nova places on host, across all projects.
//
// The listing is pinned to microversion 2.1 for the same reason serverList is:
// nova has no field selection and widens /servers/detail as the microversion
// climbs (2.3 alone adds OS-EXT-SRV-ATTR:user_data), none of which this reads.
// ID, name and status are 2.1 fields.
func hostServers(ctx context.Context, client *gophercloud.ServiceClient, host string, pin bool) ([]servers.Server, error) {
	listClient := client
	if pin {
		// setMicroversionHeader rewrites the header from client.Microversion on
		// every request, so the version is lowered on a shallow copy rather than
		// through RequestOpts (same pattern as serverActionRaw).
		pinned := *client
		pinned.Microversion = "2.1"
		listClient = &pinned
	}
	// host is an admin-only filter, and without all_tenants nova answers with
	// only the caller's own project's servers — which would silently drain half
	// a host.
	opts := servers.ListOpts{Host: host, AllTenants: true}
	all, err := paging.Collect(ctx, servers.List(listClient, opts), 0, servers.ExtractServers)
	if err != nil {
		return nil, fmt.Errorf("listing servers on host %q: %w", host, err)
	}
	return all, nil
}

// emptyHostResult decides what "no servers came back" means.
//
// An exact-match listing cannot tell an empty host from a misspelled one, and
// silently reporting "nothing to do" for a typo is the one outcome a drain must
// never produce — novaclient raises 404 here via its --strict path. So the host
// is confirmed against os-services before the empty result is accepted.
func emptyHostResult(ctx context.Context, client *gophercloud.ServiceClient, host string,
	o *output.Options, out drainOutput,
) error {
	if _, found, ok := computeServiceOn(ctx, client, host); ok && !found {
		return unknownHostError(host)
	}
	if err := o.WriteList(out.table, drainTable(nil)); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out.progress, "Host %s has no servers; nothing to move\n", host)
	return err
}

func unknownHostError(host string) error {
	return fmt.Errorf("no compute host named %q (check the Host column of `koc compute service list`)", host)
}

// computeServiceOn returns the nova-compute service record for host.
//
// os-services is the right question to ask rather than os-hypervisors: the
// `host` filter the discovery listing uses is nova's *compute service* host, so
// this checks the same name the drain searched on — and nova filters it
// server-side, which os-hypervisors cannot do below microversion 2.53.
//
// found is false when nova has no such service. ok is false when the question
// could not be answered at all — a token without the os-services policy, or a
// cloud that hides it — in which case the caller must not conclude anything
// from found.
func computeServiceOn(ctx context.Context, client *gophercloud.ServiceClient, host string) (
	svc services.Service, found, ok bool,
) {
	pages, err := services.List(client, services.ListOpts{Binary: "nova-compute", Host: host}).AllPages(ctx)
	if err != nil {
		return svc, false, false
	}
	all, err := services.ExtractServices(pages)
	if err != nil {
		return svc, false, false
	}
	if len(all) == 0 {
		return svc, false, true
	}
	return all[0], true, true
}

// planDrain splits the host's servers into the ones to move and the ones to
// report untouched, preserving nova's listing order.
func planDrain(list []servers.Server, maxServers int, mode *drainMode) *drainPlan {
	p := &drainPlan{results: make([]drainResult, len(list))}
	for i, s := range list {
		p.results[i] = drainResult{id: s.ID, name: s.Name, status: s.Status}
		switch {
		case !mode.eligible(s.Status):
			p.results[i].result = drainSkipped
			p.results[i].detail = mode.whyNot(s.Status)
		case maxServers > 0 && len(p.migrate) >= maxServers:
			p.results[i].result = drainDeferred
			p.results[i].detail = "beyond --max-servers"
		default:
			p.results[i].result = drainPlanned
			p.migrate = append(p.migrate, i)
		}
	}
	return p
}

// whyNotEligible explains a skipped server: the status it is in, and which of
// the other host drains — if any — nova would accept it for.
//
// The three verbs' accepted states barely overlap (a stopped server is
// cold-migratable and evacuable but not live-migratable; an errored one only
// evacuable), so a skipped row that only said what did not happen would leave
// the operator to work out which verb does. op is this drain's past participle
// ("live-migrated").
func whyNotEligible(op, status string) string {
	msg := fmt.Sprintf("status %s cannot be %s", status, op)
	alternatives := []struct {
		verb     string
		op       string
		eligible func(string) bool
	}{
		{"compute host drain", "live-migrated", liveMigratable},
		{"compute host drain --cold", "cold-migrated", coldMigratable},
		{"compute host evacuate", "evacuated", evacuable},
	}
	var alts []string
	for _, a := range alternatives {
		if a.op != op && a.eligible(status) {
			alts = append(alts, a.verb)
		}
	}
	if len(alts) == 0 {
		return msg
	}
	return msg + "; try " + strings.Join(alts, " or ")
}

// statusIn reports whether status is one of want, case-insensitively. Nova's
// status casing has varied across releases.
func statusIn(status string, want ...string) bool {
	for _, s := range want {
		if strings.EqualFold(status, s) {
			return true
		}
	}
	return false
}

// runDrain moves every planned server, at most f.parallel at a time, and
// records each outcome in place.
func runDrain(ctx context.Context, client *gophercloud.ServiceClient, host string, plan *drainPlan,
	f *hostDrainFlags, mode *drainMode, pr *drainProgress,
) {
	workers := min(f.parallel, len(plan.migrate))
	queue := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				r := &plan.results[i]
				r.result, r.detail = drainOne(ctx, client, host, r, f, mode, pr)
			}
		}()
	}
	for _, i := range plan.migrate {
		select {
		case <-ctx.Done():
			// Interrupted: leave the rest planned rather than claiming an
			// outcome for a move that was never posted.
			close(queue)
			wg.Wait()
			return
		case queue <- i:
		}
	}
	close(queue)
	wg.Wait()
}

// drainOne posts one server's action and, with --wait, follows it off the host.
func drainOne(ctx context.Context, client *gophercloud.ServiceClient, host string, r *drainResult,
	f *hostDrainFlags, mode *drainMode, pr *drainProgress,
) (result, detail string) {
	ref := r.displayRef()
	pr.began(ref, "starting")
	if err := serverActionNegotiated(ctx, client, r.id, mode.action); err != nil {
		return drainFail(pr, ref, err)
	}
	if !f.wait {
		pr.settled(ref, drainAccepted, "")
		return drainAccepted, ""
	}
	pr.began(ref, "moving")
	timeout := f.waitTimeout
	if timeout <= 0 {
		timeout = migratePollTimeout
	}
	status, err := awaitServerLeft(ctx, client, ref, r.id, host, r.status, timeout)
	if err != nil {
		return drainFail(pr, ref, err)
	}
	detail = "status " + status
	if mode.finish != nil {
		pr.began(ref, "finishing")
		detail, err = mode.finish(ctx, client, r.id, status)
		if err != nil {
			return drainFail(pr, ref, err)
		}
	}
	pr.settled(ref, drainCompleted, "")
	return drainCompleted, detail
}

func drainFail(pr *drainProgress, ref string, err error) (result, detail string) {
	detail = drainError(err)
	pr.settled(ref, drainFailed, detail)
	return drainFailed, detail
}

// awaitServerLeft polls until the server is no longer on host with no task in
// flight, and returns the status it settled in.
//
// "It left the host" is the exact signal, and the only one that serves all
// three drains. Matching statuses cannot: a live migration ends in the status
// it started in, a cold migration ends in VERIFY_RESIZE unless nova's
// resize_confirm_window already confirmed it (then ACTIVE, or SHUTOFF if it was
// stopped), and an evacuated STOPPED server ends SHUTOFF after passing through
// ACTIVE. Worse, ACTIVE is both where a cold migration starts and where an
// auto-confirmed one ends, so a status match would report a migration finished
// before nova had begun it.
//
// The host, by contrast, moves once and in one direction, and nova sets it on
// the source side before it ever reports VERIFY_RESIZE — see the comment in
// _finish_resize (nova/compute/manager.py) explaining that resize_instance on
// the source host already repointed instance.host at the destination.
//
// startStatus is the server's status when the drain found it, and it is what
// makes ERROR mean something: an evacuation's whole purpose is moving instances
// that are already in ERROR, so ERROR is a failure only when the server entered
// it here — or when it is still in it after reaching the destination, which is
// a rebuild that did not work.
func awaitServerLeft(ctx context.Context, client *gophercloud.ServiceClient, ref, id, host, startStatus string,
	timeout time.Duration,
) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(migratePollInterval)
	defer ticker.Stop()

	var getErrors int
	for {
		var s struct {
			Status    string `json:"status"`
			TaskState string `json:"OS-EXT-STS:task_state"`
			Host      string `json:"OS-EXT-SRV-ATTR:host"`
		}
		if err := servers.Get(ctx, client, id).ExtractInto(&s); err != nil {
			if ctx.Err() != nil {
				return "", fmt.Errorf("waiting for server %q to leave host %q: %w", ref, host, ctx.Err())
			}
			// Tolerate a few consecutive transient Get errors before giving up.
			getErrors++
			if getErrors > maxConsecutiveGetErrors {
				return "", fmt.Errorf("polling server %q during the move: %w", ref, err)
			}
		} else {
			getErrors = 0
			left := s.TaskState == "" && s.Host != "" && s.Host != host
			if err := classifyDrainState(ref, host, s.Status, startStatus, left); err != nil {
				return "", err
			}
			if left {
				return s.Status, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for server %q to leave host %q: %w", ref, host, ctx.Err())
		case <-ticker.C:
		}
	}
}

// classifyDrainState decides whether one poll of a moving server is a failure.
// It is pure, so every combination is reachable from a table test rather than
// only from a live nova transition.
//
// Three cases, and the middle one is why this is not a one-liner:
//
//   - The server reached its new host in ERROR: the move ran and the rebuild
//     failed, whatever state it started in.
//   - The server is still on the source host in ERROR, having started there in
//     ERROR: that is where an evacuation begins. Keep polling.
//   - The server is still on the source host in ERROR, having started healthy:
//     the move put it there. Fail.
func classifyDrainState(ref, host, status, startStatus string, left bool) error {
	if !statusIn(status, "ERROR") {
		return nil
	}
	if left {
		return fmt.Errorf("server %q reached its new host in ERROR status", ref)
	}
	if statusIn(startStatus, "ERROR") {
		return nil
	}
	return fmt.Errorf("server %q entered ERROR status while leaving host %q", ref, host)
}

// drainError condenses an API failure to what fits a table cell.
//
// gophercloud's ErrUnexpectedResponseCode stringifies to the request line plus
// the entire response body, which is unreadable once five of them are stacked
// in a Detail column. Nova's reason is the part that matters — "Instance is
// locked", "No valid host was found" — so it is lifted out and prefixed with
// the status, and anything that is not a nova error object falls back to the
// error's own first line.
func drainError(err error) string {
	var unexpected gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &unexpected) {
		return firstLine(err.Error())
	}
	status := fmt.Sprintf("%d %s", unexpected.Actual, http.StatusText(unexpected.Actual))
	if msg := novaFaultMessage(unexpected.Body); msg != "" {
		return status + ": " + msg
	}
	return status
}

// novaFaultMessage pulls the message out of a nova error body. Every one of
// them is a single-key object naming the fault — {"conflictingRequest":
// {"message": …}}, {"badRequest": …}, {"itemNotFound": …} — so the key is not
// worth enumerating; the first message found wins.
func novaFaultMessage(body []byte) string {
	var fault map[string]struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &fault); err != nil {
		return ""
	}
	for _, f := range fault {
		if f.Message != "" {
			return firstLine(f.Message)
		}
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// displayRef is the short, human-facing name of a server in progress output:
// its name when it has one, else its ID.
func (r *drainResult) displayRef() string {
	if r.name != "" {
		return r.name
	}
	return r.id
}

func drainTable(results []drainResult) output.Table {
	t := output.Table{
		Columns: []string{"ID", "Name", "Status", "Result", "Detail"},
		Rows:    make([][]any, 0, len(results)),
	}
	for _, r := range results {
		t.Rows = append(t.Rows, []any{r.id, r.name, r.status, r.result, r.detail})
	}
	return t
}

// drainProgress renders the drain's live status.
//
// A drain is the one koc command that can run for an hour with nothing to show
// for it, so it reports as it goes. On a terminal it repaints a single status
// line in place, on a ticker, so the elapsed time keeps moving while a long
// move is in flight; anywhere else — a pipe, a CI log, a file — it appends one
// line per state change, because a carriage return in a log is noise.
//
// It writes to stderr in both cases. The result table is the command's output
// and goes to stdout, so `-f json` stays parseable while the drain narrates.
type drainProgress struct {
	w         io.Writer
	tty       bool
	width     int
	host      string
	total     int
	startedAt time.Time

	mu       sync.Mutex
	settledN int
	failed   int
	inflight map[string]string
	painted  int
}

func newDrainProgress(w io.Writer, host string, total int) *drainProgress {
	tty, width := progressTerminal(w)
	return &drainProgress{
		w:         w,
		tty:       tty,
		width:     width,
		host:      host,
		total:     total,
		startedAt: time.Now(),
		inflight:  map[string]string{},
	}
}

// progressTerminal reports whether w is a terminal, and how wide. A writer that
// is not an *os.File — a test buffer, a pipe — is never one.
func progressTerminal(w io.Writer) (isTTY bool, width int) {
	f, ok := w.(*os.File)
	if !ok {
		return false, 0
	}
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return false, 0
	}
	if wd, _, err := term.GetSize(fd); err == nil && wd > 0 {
		return true, wd
	}
	return true, 80
}

// start begins repainting the terminal status line on a ticker and returns the
// function that stops it. Off a terminal there is nothing to repaint, so the
// ticker is never started and stop is a no-op beyond clearing.
func (p *drainProgress) start() (stop func()) {
	if !p.tty || p.total == 0 {
		return func() { /* no ticker was started, and nothing was painted to clear */ }
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(drainTickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				p.mu.Lock()
				p.paint()
				p.mu.Unlock()
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
		p.mu.Lock()
		p.clear()
		p.mu.Unlock()
	}
}

// began records that a server entered phase.
func (p *drainProgress) began(ref, phase string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inflight[ref] = phase
	if p.tty {
		p.paint()
		return
	}
	p.logf("%s: %s", ref, phase)
}

// settled records a server's final outcome.
func (p *drainProgress) settled(ref, result, detail string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inflight, ref)
	p.settledN++
	if result == drainFailed {
		p.failed++
	}
	if p.tty {
		p.paint()
		return
	}
	if detail != "" {
		p.logf("%s: %s: %s", ref, result, detail)
		return
	}
	p.logf("%s: %s", ref, result)
}

// summarize writes the closing line (and, when the drain only started the
// moves, how to follow them) and returns the command's error.
func (p *drainProgress) summarize(plan *drainPlan, f *hostDrainFlags, mode *drainMode, host string) error {
	var failed, skipped, left int
	for _, r := range plan.results {
		switch r.result {
		case drainFailed:
			failed++
		case drainSkipped:
			skipped++
		case drainDeferred:
			left++
		}
	}
	verb := "accepted"
	if f.wait {
		verb = mode.pastTense
	}
	p.logf("Host %s: %d %s, %d failed, %d skipped, %d left in place — %s",
		host, len(plan.migrate)-failed, verb, failed, skipped, left, p.elapsed())
	if skipped+left > 0 {
		// Counts what was never attempted. A failed move also leaves its server
		// on the host, but that is what the error below is for.
		p.logf("warning: %d server(s) on %s were not attempted", skipped+left, host)
	}
	if !f.wait && len(plan.migrate) > failed {
		p.logf("Not waiting; follow them with: "+
			"koc server migration list --host %s --status running --progress --watch", host)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d %ss off host %q failed", failed, len(plan.migrate), mode.name, host)
	}
	return nil
}

func (p *drainProgress) elapsed() time.Duration {
	return time.Since(p.startedAt).Truncate(time.Second)
}

// logf appends one line. Writes are best-effort: losing a progress line must
// never fail a drain that is otherwise working.
func (p *drainProgress) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(p.w, format+"\n", args...)
}

// paint repaints the one-line terminal status. The caller holds p.mu.
func (p *drainProgress) paint() {
	line := p.statusLine()
	pad := ""
	if n := p.painted - len([]rune(line)); n > 0 {
		pad = strings.Repeat(" ", n)
	}
	p.painted = len([]rune(line))
	_, _ = fmt.Fprint(p.w, "\r"+line+pad+"\r")
}

// clear erases the status line so what follows starts on a clean one. The
// caller holds p.mu.
func (p *drainProgress) clear() {
	if p.painted == 0 {
		return
	}
	_, _ = fmt.Fprint(p.w, "\r"+strings.Repeat(" ", p.painted)+"\r")
	p.painted = 0
}

// statusLine is the rendered status, truncated to the terminal width so a
// narrow window does not wrap it into a second line the repaint cannot erase.
func (p *drainProgress) statusLine() string {
	var b strings.Builder
	fmt.Fprintf(&b, "draining %s: %d/%d done", p.host, p.settledN, p.total)
	if p.failed > 0 {
		fmt.Fprintf(&b, ", %d failed", p.failed)
	}
	fmt.Fprintf(&b, " — %s", p.elapsed())
	if in := p.inflightSummary(); in != "" {
		fmt.Fprintf(&b, " (%s)", in)
	}
	return truncateRunes(b.String(), p.width-1)
}

// inflightSummary names what is happening right now: the one server in flight,
// or a count when several are.
func (p *drainProgress) inflightSummary() string {
	switch len(p.inflight) {
	case 0:
		return ""
	case 1:
		for ref, phase := range p.inflight {
			return ref + " " + phase
		}
	}
	return fmt.Sprintf("%d in flight", len(p.inflight))
}

// truncateRunes cuts s to at most limit runes, marking the cut with an
// ellipsis. A limit of zero or less means no limit.
func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	if limit == 1 {
		return "…"
	}
	return string(r[:limit-1]) + "…"
}
