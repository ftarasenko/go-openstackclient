package baremetal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
)

// ProvisionStateOpts is gophercloud's nodes.ProvisionStateOpts plus the two
// fields it does not model: runbook (API 1.92) and disable_ramdisk (1.70).
type ProvisionStateOpts struct {
	nodes.ProvisionStateOpts

	// Runbook names a runbook to run instead of explicit steps.
	Runbook string
	// DisableRamdisk skips booting ironic-python-agent, so only steps that do
	// not need it can run.
	DisableRamdisk bool
}

// ToProvisionStateMap implements nodes.ProvisionStateOptsBuilder.
func (o ProvisionStateOpts) ToProvisionStateMap() (map[string]any, error) {
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

// SetProvisionState requests a provision state transition. The service and
// unhold targets are gated (FeatureNodeService, FeatureNodeUnhold).
func SetProvisionState(ctx context.Context, c *gophercloud.ServiceClient, id string,
	opts nodes.ProvisionStateOptsBuilder,
) error {
	err := wrap(nodes.ChangeProvisionState(ctx, c, id, opts).ExtractErr(), "changing provision state of node %s", id)
	if err == nil {
		return nil
	}
	if body, berr := opts.ToProvisionStateMap(); berr == nil {
		switch nodes.TargetProvisionState(fmt.Sprint(body["target"])) {
		case nodes.TargetService:
			return ExplainMicroversion(ctx, c, FeatureNodeService, err)
		case nodes.TargetUnhold:
			return ExplainMicroversion(ctx, c, FeatureNodeUnhold, err)
		}
	}
	return err
}

// maxConsecutiveGetErrors is how many failed node reads in a row a wait
// tolerates; a successful read resets the count.
const maxConsecutiveGetErrors = 5

// WaitForProvisionState polls a node every interval until its transition
// settles in want. It fails at once on a failure state (IsProvisionFailure)
// and on a transition that settled elsewhere, and gives up when ctx is done
// (give it a deadline) or after five failed reads in a row.
//
// Settled means target_provision_state cleared: ironic clears it only once a
// transition ends, so a node already in want before the transition begins
// (rebuild of an active node) does not end the wait early.
func WaitForProvisionState(ctx context.Context, c *gophercloud.ServiceClient, id string,
	want nodes.ProvisionState, interval time.Duration,
) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var getErrors int
	var last string
	for {
		n, err := nodes.Get(ctx, c, id).Extract()
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for node %s to reach %q%s: %w", id, want, lastProvisionState(last), ctx.Err())
			}
			getErrors++
			if getErrors > maxConsecutiveGetErrors {
				return fmt.Errorf("polling node %s%s: %w", id, lastProvisionState(last), err)
			}
		} else {
			getErrors = 0
			last = n.ProvisionState
			done, cerr := classifyProvisionState(n, id, want)
			if cerr != nil {
				return cerr
			}
			if done {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for node %s to reach %q%s: %w", id, want, lastProvisionState(last), ctx.Err())
		case <-ticker.C:
		}
	}
}

// classifyProvisionState decides from one read whether the wait for want is
// over: (true, nil) settled in want, an error for a terminal wrong outcome,
// (false, nil) while the transition runs.
func classifyProvisionState(n *nodes.Node, id string, want nodes.ProvisionState) (bool, error) {
	settled := n.TargetProvisionState == ""
	switch {
	case settled && n.ProvisionState == string(want):
		return true, nil
	case IsProvisionFailure(n.ProvisionState):
		return false, fmt.Errorf("node %s entered failure state %q: %s", id, n.ProvisionState, n.LastError)
	case settled && n.ProvisionState != string(want):
		if n.LastError != "" {
			return false, fmt.Errorf("node %s settled in unexpected state %q instead of %q: %s", id, n.ProvisionState, want, n.LastError)
		}
		return false, fmt.Errorf("node %s settled in unexpected state %q instead of %q", id, n.ProvisionState, want)
	}
	return false, nil
}

// lastProvisionState renders the state last seen for a wait that gave up.
func lastProvisionState(last string) string {
	if last == "" {
		return ""
	}
	return fmt.Sprintf(" (last provision_state %q)", last)
}

// WaitForProvisionSettled polls a node every interval until its transition
// settles, and returns the state it settled in with ironic's last_error. It
// is the wait for a transition with no single destination (abort, unhold),
// so a failure state is a normal outcome here, not an error.
//
// Settled is a failure state, which ironic can leave with a target still set
// (an aborted inspection), or a cleared target_provision_state.
func WaitForProvisionSettled(ctx context.Context, c *gophercloud.ServiceClient, id string,
	interval time.Duration,
) (state, lastError string, err error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var getErrors int
	var last string
	for {
		n, err := nodes.Get(ctx, c, id).Extract()
		if err != nil {
			if ctx.Err() != nil {
				return "", "", fmt.Errorf("waiting for node %s to settle%s: %w", id, lastProvisionState(last), ctx.Err())
			}
			getErrors++
			if getErrors > maxConsecutiveGetErrors {
				return "", "", fmt.Errorf("polling node %s%s: %w", id, lastProvisionState(last), err)
			}
		} else {
			getErrors = 0
			last = n.ProvisionState
			if IsProvisionFailure(n.ProvisionState) || n.TargetProvisionState == "" {
				return n.ProvisionState, n.LastError, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", "", fmt.Errorf("waiting for node %s to settle%s: %w", id, lastProvisionState(last), ctx.Err())
		case <-ticker.C:
		}
	}
}

// IsProvisionFailure reports whether a provision state is a terminal failure:
// "error", or any "<verb> failed", as python-ironicclient decides it.
func IsProvisionFailure(state string) bool {
	return strings.HasSuffix(state, " failed") || state == string(nodes.Error)
}
