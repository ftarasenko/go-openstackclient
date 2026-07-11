package baremetal

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
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

// provisionTransitions enumerates the supported provision-state verbs, mirroring
// upstream `openstack baremetal node <verb>`.
func provisionTransitions() []provisionTransition {
	return []provisionTransition{
		{"manage", "Set a node to the manageable provision state", nodes.TargetManage, nodes.Manageable},
		{"provide", "Make a node available for deployment", nodes.TargetProvide, nodes.Available},
		{"deploy", "Deploy a node (set to active)", nodes.TargetActive, nodes.Active},
		{"undeploy", "Undeploy a node (tear down to available)", nodes.TargetDeleted, nodes.Available},
		{"rebuild", "Rebuild a node", nodes.TargetRebuild, nodes.Active},
		{"inspect", "Inspect a node's hardware", nodes.TargetInspect, nodes.Manageable},
	}
}

// newNodeProvisionCommands builds the provision-state transition subcommands.
func newNodeProvisionCommands(a *auth.Options, o *output.Options) []*cobra.Command {
	var cmds []*cobra.Command
	for _, tr := range provisionTransitions() {
		tr := tr
		var wait bool
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
				return runNodeProvision(ctx, client, tr, args[0], wait, cmd.OutOrStdout())
			},
		}
		// --wait polls node.ProvisionState until the target stable state (or a
		// failure state). Flag semantics follow upstream OSC; UNVERIFIED against
		// KeyStack docs (docs.keystack.ru returned HTTP 403).
		cmd.Flags().BoolVar(&wait, "wait", false, "wait until the provision-state transition completes")
		cmds = append(cmds, cmd)
	}
	return cmds
}

func runNodeProvision(ctx context.Context, client *gophercloud.ServiceClient, tr provisionTransition, id string, wait bool, w io.Writer) error {
	opts := nodes.ProvisionStateOpts{Target: tr.target}
	if err := nodes.ChangeProvisionState(ctx, client, id, opts).ExtractErr(); err != nil {
		return fmt.Errorf("requesting %s on node %s: %w", tr.verb, id, err)
	}
	if !wait {
		if _, err := fmt.Fprintf(w, "Requested %s for node %s\n", tr.verb, id); err != nil {
			return err
		}
		return nil
	}
	if err := waitForProvisionState(ctx, client, id, tr.wantState); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Node %s reached provision state %q\n", id, tr.wantState); err != nil {
		return err
	}
	return nil
}

// maxConsecutiveGetErrors bounds how many CONSECUTIVE nodes.Get failures the
// --wait poll tolerates before giving up; the counter resets on any success.
const maxConsecutiveGetErrors = 5

// waitForProvisionState polls the node until it reaches want, or a terminal
// failure/error state, or the timeout/context expires. Unlike gophercloud's
// nodes.WaitForProvisionState it fails fast on error states instead of spinning
// until timeout.
//
// Success and failure are keyed off ironic's target_provision_state: ironic
// clears it only once a transition settles. This avoids returning immediately
// when the node's starting ProvisionState already equals want (e.g. rebuild of
// an "active" node, inspect of a "manageable" node) before ironic has begun the
// transition, and avoids hanging the full timeout when a transition settles into
// an unexpected state (e.g. manage verify failure → "enroll").
func waitForProvisionState(ctx context.Context, client *gophercloud.ServiceClient, id string, want nodes.ProvisionState) error {
	ctx, cancel := context.WithTimeout(ctx, provisionPollTimeout)
	defer cancel()

	ticker := time.NewTicker(provisionPollInterval)
	defer ticker.Stop()

	var getErrors int
	for {
		n, err := nodes.Get(ctx, client, id).Extract()
		if err != nil {
			// Tolerate a small number of consecutive transient Get errors, but
			// stop promptly if the context is done.
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for node %s to reach %q: %w", id, want, ctx.Err())
			}
			getErrors++
			if getErrors > maxConsecutiveGetErrors {
				return fmt.Errorf("polling node %s: %w", id, err)
			}
		} else {
			getErrors = 0
			settled := n.TargetProvisionState == ""
			switch {
			case settled && n.ProvisionState == string(want):
				return nil
			case isProvisionFailure(n.ProvisionState):
				return fmt.Errorf("node %s entered failure state %q: %s", id, n.ProvisionState, n.LastError)
			case settled && n.ProvisionState != string(want) && n.LastError != "":
				return fmt.Errorf("node %s settled in unexpected state %q instead of %q: %s", id, n.ProvisionState, want, n.LastError)
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for node %s to reach %q: %w", id, want, ctx.Err())
		case <-ticker.C:
		}
	}
}

// isProvisionFailure reports whether a provision state is a terminal failure.
func isProvisionFailure(state string) bool {
	return strings.Contains(state, "failed") || state == string(nodes.Error)
}
