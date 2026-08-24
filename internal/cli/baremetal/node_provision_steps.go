package baremetal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The provision-state verbs that carry a payload: clean, service and rescue.
// They share the plumbing in node_provision.go (--wait, the settle poll) and
// differ only in what goes in the request body.
//
// Flag names mirror upstream python-ironicclient
// (`openstack baremetal node clean|service|rescue`). The KeyStack command
// reference at https://docs.keystack.ru/ was not reachable at implementation
// time (HTTP 403), so these are UNVERIFIED against KeyStack and fall back to
// upstream semantics.

// provisionStateOpts extends gophercloud's ProvisionStateOpts with the two
// fields it does not model: `runbook` (ironic API 1.92) and `disable_ramdisk`
// (1.70). Implementing ProvisionStateOptsBuilder over the typed struct keeps the
// call itself typed — only the two extra keys are hand-written.
type provisionStateOpts struct {
	nodes.ProvisionStateOpts

	// Runbook names a predefined runbook to run instead of explicit steps.
	Runbook string
	// DisableRamdisk skips booting ironic-python-agent, so only steps marked as
	// not requiring it can run.
	DisableRamdisk bool
}

func (o provisionStateOpts) ToProvisionStateMap() (map[string]any, error) {
	body, err := o.ProvisionStateOpts.ToProvisionStateMap()
	if err != nil {
		return nil, err
	}
	if o.Runbook != "" {
		body["runbook"] = o.Runbook
	}
	if o.DisableRamdisk {
		body["disable_ramdisk"] = true
	}
	return body, nil
}

// stepsFlags is the shared --<verb>-steps / --runbook / --disable-ramdisk
// surface of `node clean` and `node service`.
type stepsFlags struct {
	steps          string
	runbook        string
	disableRamdisk bool
}

// addStepsFlags registers the trio on cmd. stepsFlag is "clean-steps" or
// "service-steps"; upstream makes steps and runbook a mutually exclusive
// required pair.
func addStepsFlags(cmd *cobra.Command, f *stepsFlags, stepsFlag, noun string) {
	fl := cmd.Flags()
	fl.StringVar(&f.steps, stepsFlag, "",
		fmt.Sprintf("the %s steps: a JSON or YAML string, a path to a JSON or YAML file, or '-' to read standard input", noun))
	fl.StringVar(&f.runbook, "runbook", "",
		fmt.Sprintf("identifier of a predefined runbook to use for %s (requires ironic API 1.92)", noun))
	fl.BoolVar(&f.disableRamdisk, "disable-ramdisk", false,
		"do not boot ironic-python-agent; only steps marked as not requiring it can run")
	cmd.MarkFlagsMutuallyExclusive(stepsFlag, "runbook")
	cmd.MarkFlagsOneRequired(stepsFlag, "runbook")
}

// resolve turns the flag values into the request payload, reading and parsing
// the steps document when one was given.
func (f *stepsFlags) resolve(stdin io.Reader) ([]map[string]any, error) {
	if f.steps == "" {
		return nil, nil
	}
	raw, err := readStepsDocument(f.steps, stdin)
	if err != nil {
		return nil, err
	}
	return parseSteps(raw)
}

// readStepsDocument resolves the argument to bytes: '-' is standard input, a
// value that parses as a JSON/YAML list is used literally, and anything else is
// treated as a path.
//
// The order matters: checking for an inline document first means a literal
// "[...]" is never mistaken for a filename, and checking the file second means a
// path is never mistaken for a (failing) document.
func readStepsDocument(arg string, stdin io.Reader) ([]byte, error) {
	if arg == "-" {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("reading steps from standard input: %w", err)
		}
		return raw, nil
	}
	if trimmed := strings.TrimSpace(arg); strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "-") {
		return []byte(arg), nil
	}
	raw, err := os.ReadFile(arg) //nolint:gosec // G304: operator-supplied steps file path
	if err != nil {
		return nil, fmt.Errorf("reading steps file %q: %w", arg, err)
	}
	return raw, nil
}

// parseSteps decodes a steps document. YAML is a superset of JSON, so one
// yaml.v3 pass accepts both forms upstream documents; yaml.v3 (unlike v2) yields
// map[string]any, so the result re-encodes to JSON unchanged.
func parseSteps(raw []byte) ([]map[string]any, error) {
	var steps []map[string]any
	if err := yaml.Unmarshal(raw, &steps); err != nil {
		return nil, fmt.Errorf("parsing steps: %w", err)
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("steps document is empty; expected a list of {interface, step} objects")
	}
	for i, s := range steps {
		if _, ok := s["interface"]; !ok {
			return nil, fmt.Errorf("step %d is missing the required key %q", i+1, "interface")
		}
		if _, ok := s["step"]; !ok {
			return nil, fmt.Errorf("step %d is missing the required key %q", i+1, "step")
		}
	}
	return steps, nil
}

// toCleanSteps converts the parsed generic steps into gophercloud's typed form.
// The round trip through JSON keeps `args` intact whatever it holds, which
// matters because its shape is driver-defined.
func toCleanSteps(steps []map[string]any) ([]nodes.CleanStep, error) {
	var out []nodes.CleanStep
	if err := remarshal(steps, &out); err != nil {
		return nil, fmt.Errorf("converting clean steps: %w", err)
	}
	return out, nil
}

func toServiceSteps(steps []map[string]any) ([]nodes.ServiceStep, error) {
	var out []nodes.ServiceStep
	if err := remarshal(steps, &out); err != nil {
		return nil, fmt.Errorf("converting service steps: %w", err)
	}
	return out, nil
}

func remarshal(from any, to any) error {
	raw, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, to)
}

// --- clean ------------------------------------------------------------------

func newNodeCleanCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &stepsFlags{}
	wf := &waitFlags{}
	cmd := &cobra.Command{
		Use:   "clean <node>",
		Short: "Run manual cleaning steps on a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeClean(ctx, client, args[0], f, wf, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	addStepsFlags(cmd, f, "clean-steps", "clean")
	addWaitFlags(cmd, wf, "cleaning")
	return cmd
}

func runNodeClean(ctx context.Context, client *gophercloud.ServiceClient, id string,
	f *stepsFlags, wf *waitFlags, stdin io.Reader, w io.Writer,
) error {
	parsed, err := f.resolve(stdin)
	if err != nil {
		return err
	}
	opts := provisionStateOpts{
		ProvisionStateOpts: nodes.ProvisionStateOpts{Target: nodes.TargetClean},
		Runbook:            f.runbook,
		DisableRamdisk:     f.disableRamdisk,
	}
	if parsed != nil {
		if opts.CleanSteps, err = toCleanSteps(parsed); err != nil {
			return err
		}
	}
	return applyProvisionState(ctx, client, id,
		provisionRequest{verb: "clean", opts: opts, want: nodes.Manageable, wait: wf}, w)
}

// --- service ----------------------------------------------------------------

func newNodeServiceCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &stepsFlags{}
	wf := &waitFlags{}
	cmd := &cobra.Command{
		Use:   "service <node>",
		Short: "Run service steps on an active node (requires ironic API 1.87)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeService(ctx, client, args[0], f, wf, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	addStepsFlags(cmd, f, "service-steps", "service")
	addWaitFlags(cmd, wf, "servicing")
	return cmd
}

func runNodeService(ctx context.Context, client *gophercloud.ServiceClient, id string,
	f *stepsFlags, wf *waitFlags, stdin io.Reader, w io.Writer,
) error {
	parsed, err := f.resolve(stdin)
	if err != nil {
		return err
	}
	opts := provisionStateOpts{
		ProvisionStateOpts: nodes.ProvisionStateOpts{Target: nodes.TargetService},
		Runbook:            f.runbook,
		DisableRamdisk:     f.disableRamdisk,
	}
	if parsed != nil {
		if opts.ServiceSteps, err = toServiceSteps(parsed); err != nil {
			return err
		}
	}
	// The `service` target arrived at 1.87, above the Zed cap of 1.82, so an old
	// cloud rejects the value rather than the version — see microversion.go.
	err = applyProvisionState(ctx, client, id,
		provisionRequest{verb: "service", opts: opts, want: nodes.Active, wait: wf}, w)
	return explainMicroversion(ctx, client, featureNodeService, err)
}

// --- rescue / unhold --------------------------------------------------------

func newNodeRescueCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var password string
	wf := &waitFlags{}
	cmd := &cobra.Command{
		Use:   "rescue <node>",
		Short: "Boot a node into the rescue ramdisk",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeRescue(ctx, client, args[0], password, wf, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&password, "rescue-password", "", "password for logging in to the rescue ramdisk")
	_ = cmd.MarkFlagRequired("rescue-password")
	addWaitFlags(cmd, wf, "the rescue")
	return cmd
}

func runNodeRescue(ctx context.Context, client *gophercloud.ServiceClient, id, password string,
	wf *waitFlags, w io.Writer,
) error {
	opts := nodes.ProvisionStateOpts{Target: nodes.TargetRescue, RescuePassword: password}
	return applyProvisionState(ctx, client, id,
		provisionRequest{verb: "rescue", opts: opts, want: nodes.Rescue, wait: wf}, w)
}

// newNodeUnholdCommand builds "baremetal node unhold". Like abort it has no
// single destination: releasing a "clean hold" resumes cleaning and ends at
// manageable, releasing a "deploy hold" resumes deploying and ends at active. So
// --wait waits for the transition to settle and reports where it landed.
func newNodeUnholdCommand(a *auth.Options, o *output.Options) *cobra.Command {
	wf := &waitFlags{}
	cmd := &cobra.Command{
		Use:   "unhold <node>",
		Short: "Release a node from a clean or deploy hold (requires ironic API 1.85)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeUnhold(ctx, client, args[0], wf, cmd.OutOrStdout())
		},
	}
	addWaitFlags(cmd, wf, "the hold to be released")
	return cmd
}

func runNodeUnhold(ctx context.Context, client *gophercloud.ServiceClient, id string,
	wf *waitFlags, w io.Writer,
) error {
	opts := nodes.ProvisionStateOpts{Target: nodes.TargetUnhold}
	if err := nodes.ChangeProvisionState(ctx, client, id, opts).ExtractErr(); err != nil {
		return explainMicroversion(ctx, client, featureNodeUnhold,
			fmt.Errorf("requesting unhold on node %s: %w", id, err))
	}
	if !wf.wait {
		_, err := fmt.Fprintf(w, "Requested unhold for node %s\n", id)
		return err
	}
	state, lastError, err := waitForProvisionSettled(ctx, client, id, wf.timeout)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Node %s settled in provision state %q\n", id, state); err != nil {
		return err
	}
	if lastError != "" {
		if _, err := fmt.Fprintf(w, "Last error: %s\n", lastError); err != nil {
			return err
		}
	}
	return nil
}

// --- shared -----------------------------------------------------------------

// provisionRequest is one provision-state transition: the payload to send, the
// verb naming it in messages, and the state --wait polls for.
type provisionRequest struct {
	verb string
	opts nodes.ProvisionStateOptsBuilder
	want nodes.ProvisionState
	wait *waitFlags
}

// applyProvisionState requests a transition and, with --wait, polls until the
// node reaches r.want. It is runNodeProvision generalised over an opts builder,
// so the verbs with a payload get identical --wait behaviour.
func applyProvisionState(ctx context.Context, client *gophercloud.ServiceClient, id string,
	r provisionRequest, w io.Writer,
) error {
	if err := nodes.ChangeProvisionState(ctx, client, id, r.opts).ExtractErr(); err != nil {
		return fmt.Errorf("requesting %s on node %s: %w", r.verb, id, err)
	}
	if !r.wait.wait {
		_, err := fmt.Fprintf(w, "Requested %s for node %s\n", r.verb, id)
		return err
	}
	if err := waitForProvisionState(ctx, client, id, r.want, r.wait.timeout); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "Node %s reached provision state %q\n", id, r.want)
	return err
}

// waitFlags is the --wait/--wait-timeout pair shared by every provision-state
// verb.
type waitFlags struct {
	wait    bool
	timeout time.Duration
}

// addWaitFlags registers the pair on cmd.
func addWaitFlags(cmd *cobra.Command, f *waitFlags, what string) {
	cmd.Flags().BoolVar(&f.wait, "wait", false, "wait until "+what+" completes")
	cmd.Flags().DurationVar(&f.timeout, "wait-timeout", provisionPollTimeout, "maximum time to wait for --wait to complete")
}
