package baremetal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	"github.com/ftarasenko/koc/internal/output"
	bmapi "github.com/ftarasenko/koc/pkg/openstack/baremetal"
)

// provisionPollInterval and provisionPollTimeout bound the --wait polling loop.
var (
	provisionPollInterval = 5 * time.Second
	provisionPollTimeout  = 30 * time.Minute
)

// provisionTransition describes one provision-state verb: the CLI name, the
// ironic target action, and the stable ProvisionState reached on success.
type provisionTransition struct {
	verb   string
	short  string
	target nodes.TargetProvisionState
	// wantState is the stable provision state the node settles into on success,
	// used to satisfy --wait.
	wantState nodes.ProvisionState
}

// provisionTransitions enumerates the provision-state verbs that take no verb
// specific flags, mirroring upstream `openstack baremetal node <verb>`. The
// verbs with their own payload (clean, service, rescue) and the one with no
// single destination state (unhold) are built separately below.
func provisionTransitions() []provisionTransition {
	return []provisionTransition{
		{"manage", "Set a node to the manageable provision state", nodes.TargetManage, nodes.Manageable},
		{"provide", "Make a node available for deployment", nodes.TargetProvide, nodes.Available},
		{"deploy", "Deploy a node (set to active)", nodes.TargetActive, nodes.Active},
		{"undeploy", "Undeploy a node (tear down to available)", nodes.TargetDeleted, nodes.Available},
		{"rebuild", "Rebuild a node", nodes.TargetRebuild, nodes.Active},
		{"inspect", "Inspect a node's hardware", nodes.TargetInspect, nodes.Manageable},
		{"adopt", "Adopt an already-provisioned node into ironic", nodes.TargetAdopt, nodes.Active},
		{"unrescue", "Return a rescued node to active", nodes.TargetUnrescue, nodes.Active},
	}
}

// newNodeProvisionCommands builds the provision-state transition subcommands.
func newNodeProvisionCommands(a *auth.Options, o *output.Options) []*cobra.Command {
	var cmds []*cobra.Command
	for _, tr := range provisionTransitions() {
		var wait bool
		var waitTimeout time.Duration
		cmd := &cobra.Command{
			Use:   tr.verb + " <node>",
			Short: tr.short,
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
				return runNodeProvision(ctx, client, tr, args[0], wait, waitTimeout, cmd.OutOrStdout())
			},
		}
		// --wait polls node.ProvisionState until the target stable state (or a
		// failure state). Flag semantics follow upstream OSC; UNVERIFIED against
		// KeyStack docs (docs.keystack.ru returned HTTP 403).
		cmd.Flags().BoolVar(&wait, "wait", false, "wait until the provision-state transition completes")
		cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", provisionPollTimeout, "maximum time to wait for --wait to complete")
		cmds = append(cmds, cmd)
	}
	return cmds
}

// newNodeAbortCommand builds "baremetal node abort". Unlike the transitions
// above, abort has no single stable destination state — aborting a deploy lands
// in "deploy failed", aborting a clean in "clean failed", aborting an
// inspection in "inspect failed" — so --wait waits for the transition to settle
// (target_provision_state cleared) and reports whichever state it settled in.
func newNodeAbortCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var wait bool
	var waitTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "abort <node>",
		Short: "Abort a node's current provision-state operation",
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
			return runNodeAbort(ctx, client, args[0], wait, waitTimeout, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the abort settles into a stable provision state")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", provisionPollTimeout, "maximum time to wait for --wait to complete")
	return cmd
}

func runNodeAbort(ctx context.Context, client *gophercloud.ServiceClient, id string, wait bool, waitTimeout time.Duration, w io.Writer) error {
	opts := nodes.ProvisionStateOpts{Target: nodes.TargetAbort}
	if err := bmapi.SetProvisionState(ctx, client, id, opts); err != nil {
		return provisionErr("abort", id, err)
	}
	if !wait {
		if _, err := fmt.Fprintf(w, "Requested abort for node %s\n", id); err != nil {
			return err
		}
		return nil
	}
	state, lastError, err := waitForProvisionSettled(ctx, client, id, waitTimeout)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Node %s settled in provision state %q\n", id, state); err != nil {
		return err
	}
	// A "* failed" state is the normal outcome of a successful abort (ironic
	// records why in last_error), so surface it without failing the command.
	if lastError != "" {
		if _, err := fmt.Fprintf(w, "Last error: %s\n", lastError); err != nil {
			return err
		}
	}
	return nil
}

func runNodeProvision(ctx context.Context, client *gophercloud.ServiceClient, tr provisionTransition, id string, wait bool, waitTimeout time.Duration, w io.Writer) error {
	opts := nodes.ProvisionStateOpts{Target: tr.target}
	if err := bmapi.SetProvisionState(ctx, client, id, opts); err != nil {
		return provisionErr(tr.verb, id, err)
	}
	if !wait {
		if _, err := fmt.Fprintf(w, "Requested %s for node %s\n", tr.verb, id); err != nil {
			return err
		}
		return nil
	}
	if err := waitForProvisionState(ctx, client, id, tr.wantState, waitTimeout); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Node %s reached provision state %q\n", id, tr.wantState); err != nil {
		return err
	}
	return nil
}

// waitForProvisionState is bmapi.WaitForProvisionState with --wait-timeout
// as its deadline (provisionPollTimeout when unset).
func waitForProvisionState(ctx context.Context, client *gophercloud.ServiceClient, id string, want nodes.ProvisionState, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = provisionPollTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return bmapi.WaitForProvisionState(ctx, client, id, want, provisionPollInterval)
}

// waitForProvisionSettled is bmapi.WaitForProvisionSettled with
// --wait-timeout as its deadline.
func waitForProvisionSettled(ctx context.Context, client *gophercloud.ServiceClient, id string, timeout time.Duration) (state string, lastError string, err error) {
	if timeout <= 0 {
		timeout = provisionPollTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return bmapi.WaitForProvisionSettled(ctx, client, id, provisionPollInterval)
}

// provisionErr names the CLI verb in a failed transition request, as koc
// always has: "requesting <verb> on node <id>".
func provisionErr(verb, id string, err error) error {
	var me *bmapi.MicroversionError
	if errors.As(err, &me) {
		relabelled := *me
		relabelled.Err = fmt.Errorf("requesting %s on node %s: %w", verb, id, errors.Unwrap(me.Err))
		return explained(&relabelled)
	}
	return fmt.Errorf("requesting %s on node %s: %w", verb, id, errors.Unwrap(err))
}
