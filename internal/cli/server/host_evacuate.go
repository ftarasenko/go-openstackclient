package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "compute host evacuate" — rebuild every server of a *failed* compute host on
// other hosts. python-novaclient's `nova host-evacuate`
// (novaclient/v2/shell.py do_host_evacuate).
//
// This is the disaster-recovery drain, not the maintenance one, and nova
// enforces the difference: compute_api.evacuate reads the source host's service
// record and raises ComputeServiceInUse unless it is down. So the precondition
// is the exact opposite of the other two verbs' — which is why koc checks it
// once, up front, rather than collecting the same 409 once per server.

// hostEvacuateFlags holds the options accepted by "compute host evacuate".
type hostEvacuateFlags struct {
	hostDrainFlags
	targetHost        string
	preserveEphemeral bool
	force             bool
}

func newComputeHostEvacuateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &hostEvacuateFlags{}
	cmd := &cobra.Command{
		Use:   "evacuate <host>",
		Short: "Rebuild every server of a failed compute host on other hosts",
		Long: strings.TrimSpace(`
Evacuate every server nova places on <host> — rebuilding each one on another
host — after the host itself has failed.

This is not the maintenance drain. Nova requires the source host's nova-compute
service to be DOWN and rejects the evacuation of a server whose host is still
up, so for a host you are about to take down deliberately use "compute host
drain" (no downtime) or "compute host drain --cold" (with downtime) instead. koc checks the service state once, before posting anything, rather
than collecting the same refusal for every server on the host.

An evacuated server is rebuilt from its image or boot volume on the destination:
anything on an ephemeral disk that the destination cannot reach is lost unless
the cloud puts it on shared storage. ACTIVE, SHUTOFF and ERROR servers can be
evacuated; a server that was stopped is still stopped afterwards. The command
exits non-zero if any evacuation fails.

--force skips the scheduler's verification of --target-host. Nova removed it at
microversion 2.68, so it only reaches clouds negotiating 2.29 to 2.67.`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if err := f.validate(); err != nil {
				return err
			}
			if f.force && f.targetHost == "" {
				return fmt.Errorf("--force has no effect without --target-host")
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			f.pinMicroversion = a.ComputeAPIVersionPinnable()
			progress := cmd.ErrOrStderr()
			mode, err := evacuateDrainMode(client, f, progress)
			if err != nil {
				return err
			}
			return runHostDrain(ctx, client, o, args[0], &f.hostDrainFlags, mode, cmd.OutOrStdout(), progress)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.targetHost, "target-host", "",
		"evacuate every server to this host (omit to let the scheduler choose per server)")
	fl.BoolVar(&f.preserveEphemeral, "preserve-ephemeral", false,
		"KeyStack: preserve the ephemeral partition during evacuation")
	fl.BoolVar(&f.force, "force", false,
		"skip the scheduler's verification of --target-host (compute API 2.29 to 2.67)")
	registerHostDrainFlags(cmd, &f.hostDrainFlags, "evacuated")
	return cmd
}

func evacuateDrainMode(client *gophercloud.ServiceClient, f *hostEvacuateFlags, progress writerTo) (*drainMode, error) {
	action, err := evacuateBody(client, f, progress)
	if err != nil {
		return nil, err
	}
	return &drainMode{
		name:      "evacuation",
		pastTense: "evacuated",
		eligible:  evacuable,
		whyNot: func(status string) string {
			return whyNotEligible("evacuated", status)
		},
		precheck: requireComputeServiceDown,
		action:   map[string]any{"evacuate": action},
	}, nil
}

// writerTo names the writer the body builder warns through. It exists so the
// signature reads as "somewhere to warn", matching liveMigrateBody's.
type writerTo = interface{ Write(p []byte) (int, error) }

// evacuateBody builds the evacuate action, shared by every server in the run.
//
// gophercloud's servers.EvacuateOpts is frozen at the pre-2.14 shape — it
// always emits onSharedStorage and lacks preserve_ephemeral — which nova
// rejects at the "latest" koc negotiates, so the body is built directly here
// exactly as runServerEvacuate does for the single-server verb. adminPass is
// deliberately absent: one password across a host's worth of servers is not a
// thing worth offering, and novaclient's host-evacuate does not offer it either.
func evacuateBody(client *gophercloud.ServiceClient, f *hostEvacuateFlags, progress writerTo) (map[string]any, error) {
	action := map[string]any{}
	if f.targetHost != "" {
		action["host"] = f.targetHost
	}
	if f.preserveEphemeral {
		action["preserve_ephemeral"] = true
	}
	if f.force {
		// force was added at 2.29 and removed at 2.68. Sending it above that
		// makes nova reject the whole request, so it is dropped with a warning
		// rather than failing a drain over a flag that no longer exists.
		if computeSupportsMicroversion(client, "2.68") {
			if _, err := fmt.Fprintln(progress,
				"warning: --force was removed from the evacuate action at compute API 2.68; ignoring"); err != nil {
				return nil, err
			}
		} else {
			action["force"] = true
		}
	}
	return action, nil
}

// evacuable reports whether nova will accept an evacuation for a server in this
// status. nova/compute/api.py decorates evacuate with
// @check_instance_state(vm_state=[vm_states.ACTIVE, vm_states.STOPPED,
// vm_states.ERROR], task_state=None) — ERROR included, because evacuation is
// what you reach for once instances have failed with their host.
func evacuable(status string) bool {
	return statusIn(status, "ACTIVE", "SHUTOFF", "ERROR")
}

// requireComputeServiceDown refuses to start an evacuation while nova still
// considers the host alive.
//
// compute_api.evacuate raises ComputeServiceInUse when the source service is
// up, so without this every server on the host would come back as its own
// identical 409 and the operator would have to read a table to learn one fact
// about the host. A service koc cannot read is not an obstacle — nova will
// still enforce it — so an unanswerable check lets the drain proceed.
func requireComputeServiceDown(ctx context.Context, client *gophercloud.ServiceClient, host string) error {
	svc, found, ok := computeServiceOn(ctx, client, host)
	if !ok {
		return nil
	}
	if !found {
		return unknownHostError(host)
	}
	if strings.EqualFold(svc.State, "up") {
		return fmt.Errorf("compute service on host %q is still up; nova refuses to evacuate a live host.\n"+
			"Use `koc compute host drain %s` to empty it without downtime, "+
			"`koc compute host drain %s --cold` to empty it with a reboot, or wait for the service "+
			"to be reported down", host, host, host)
	}
	return nil
}
