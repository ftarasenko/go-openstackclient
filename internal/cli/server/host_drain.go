package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "compute host drain" — empty a compute host that is still up, by migrating
// every server off it: live by default, cold with --cold.
//
// python-novaclient spells these as two commands, `nova host-evacuate-live` and
// `nova host-servers-migrate` (novaclient/v2/shell.py). koc folds them into one
// verb and a flag for the same reason `server migrate` already does: they are
// the same operation — move a running server to another host while its own host
// is healthy — differing only in whether the guest stays up. `compute host
// evacuate` stays a separate verb because it is not that operation at all; it
// requires the host to be *down*.
//
// Spelling the pair as `evacuate-live` and `evacuate` (novaclient's names) puts
// two commands with opposite preconditions next to each other in --help, where
// the one an operator reaches for during a maintenance window is the one nova
// refuses to run on a live host.

// hostDrainVerbFlags holds the options accepted by "compute host drain". The
// live-only and cold-only groups are rejected against the wrong mode rather
// than silently ignored.
type hostDrainVerbFlags struct {
	hostDrainFlags
	cold       bool
	targetHost string

	// live only
	blockMigration  bool
	sharedMigration bool
	diskOverCommit  bool

	// cold only
	confirm bool
}

// liveOnlyFlags and coldOnlyFlags name the per-mode flags, for the "requires
// --cold" / "not with --cold" checks. Declared here rather than spelled out at
// the two call sites so a new flag cannot be added to one and forgotten in the
// other.
var (
	liveOnlyFlags = []string{"block-migration", "shared-migration", "disk-overcommit"}
	coldOnlyFlags = []string{"confirm"}
)

func (f *hostDrainVerbFlags) validate(fl flagChecker) error {
	wrong, requires := liveOnlyFlags, "are only valid without --cold"
	if !f.cold {
		wrong, requires = coldOnlyFlags, "requires --cold"
	}
	for _, name := range wrong {
		if fl.Changed(name) {
			return fmt.Errorf("--%s %s", name, requires)
		}
	}
	if f.blockMigration && f.sharedMigration {
		return errors.New("--block-migration and --shared-migration are mutually exclusive")
	}
	return f.hostDrainFlags.validate()
}

// flagChecker is the slice of *pflag.FlagSet the validation needs, so the rules
// are testable without building a cobra command.
type flagChecker interface{ Changed(name string) bool }

// migrateFlags projects the drain onto the per-server "server migrate
// --live-migration" flag struct, so both verbs build the identical
// os-migrateLive body from liveMigrateBody rather than from two copies of the
// block_migration/disk_over_commit rules.
func (f *hostDrainVerbFlags) migrateFlags() *serverMigrateFlags {
	return &serverMigrateFlags{
		live:            true,
		host:            f.targetHost,
		blockMigration:  f.blockMigration,
		sharedMigration: f.sharedMigration,
		diskOverCommit:  f.diskOverCommit,
		wait:            f.wait,
		waitTimeout:     f.waitTimeout,
	}
}

func newComputeHostDrainCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &hostDrainVerbFlags{}
	cmd := &cobra.Command{
		Use:   "drain <host>",
		Short: "Migrate every server off a compute host (live by default; --cold for a cold migration)",
		Long: strings.TrimSpace(`
Migrate every server nova places on <host> to other hosts, so the host can be
taken down for maintenance.

By default each server is live-migrated, which does not interrupt the guest.
--cold shuts each one down, moves it and starts it again, for the cases a live
migration cannot cover (local disks the destination cannot reach, an instance
nova refuses to migrate live).

<host> is matched exactly against the compute service host, as reported in the
Host column of "koc compute service list" and "koc server list --long". The
host must still be up; for one that has failed, use "compute host evacuate".

Which servers can move depends on the mode — nova accepts a live migration for
ACTIVE and PAUSED servers, and a cold one for ACTIVE and SHUTOFF. Anything else
is reported as skipped, naming the verb that would take it, rather than
attempted. A locked server is refused by nova. The command exits non-zero if
any migration fails.

A cold migration does not finish on its own: nova leaves the server in
VERIFY_RESIZE awaiting a confirm or a revert. --cold confirms each server once
it lands, so the host is actually empty when the command returns;
--confirm=false leaves them pending, to be confirmed with "koc server resize
--confirm" or undone with --revert. Confirming means waiting, so --cold implies
--wait. A cloud with nova's resize_confirm_window set may confirm a server
before koc gets to it; that is reported, not treated as an error.

Without --wait a live drain returns as soon as nova has accepted every
migration, which is not the same as the host being empty.`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if err := f.validate(cmd.Flags()); err != nil {
				return err
			}
			// Confirming a server means seeing it reach VERIFY_RESIZE, so a
			// confirming drain cannot be fire-and-forget.
			if f.cold && f.confirm {
				f.wait = true
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			f.pinMicroversion = a.ComputeAPIVersionPinnable()
			progress := cmd.ErrOrStderr()
			mode, err := drainModeFor(client, f, progress)
			if err != nil {
				return err
			}
			return runHostDrain(ctx, client, o, args[0], &f.hostDrainFlags, mode, drainOutput{table: cmd.OutOrStdout(), progress: progress})
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.cold, "cold", false,
		"cold-migrate instead of live-migrating: each server is shut down, moved and started again")
	fl.StringVar(&f.targetHost, "target-host", "",
		"move every server to this host (omit to let the scheduler choose per server; --cold needs nova 2.56 or later)")
	fl.BoolVar(&f.blockMigration, "block-migration", false, "force a block migration (copy local disks)")
	fl.BoolVar(&f.sharedMigration, "shared-migration", false, "force a shared-storage migration (no disk copy)")
	fl.BoolVar(&f.diskOverCommit, "disk-overcommit", false,
		"allow disk over-commit on the destination (compute API <= 2.24)")
	fl.BoolVar(&f.confirm, "confirm", true,
		"--cold: confirm each migration once the server lands; --confirm=false leaves them in VERIFY_RESIZE")
	registerHostDrainFlags(cmd, &f.hostDrainFlags, "moved")
	return cmd
}

// drainModeFor builds the mode the drain runs in. The live body is built once
// here rather than per server, which is also what keeps the --disk-overcommit
// warning liveMigrateBody may emit to a single line.
func drainModeFor(client *gophercloud.ServiceClient, f *hostDrainVerbFlags, progress io.Writer) (*drainMode, error) {
	if f.cold {
		return coldDrainMode(f), nil
	}
	live, err := liveMigrateBody(client, f.migrateFlags(), progress)
	if err != nil {
		return nil, err
	}
	return liveDrainMode(live), nil
}

func liveDrainMode(live map[string]any) *drainMode {
	return &drainMode{
		name:      "live migration",
		pastTense: "migrated",
		eligible:  liveMigratable,
		whyNot: func(status string) string {
			return whyNotEligible("live-migrated", status)
		},
		precheck: requireComputeServiceUp,
		action:   map[string]any{"os-migrateLive": live},
	}
}

// liveMigratable reports whether nova will accept a live migration for a server
// in this status. ACTIVE and PAUSED are the two it allows — nova/compute/api.py
// decorates live_migrate with
// @check_instance_state(vm_state=[vm_states.ACTIVE, vm_states.PAUSED]) — and
// everything else (SHUTOFF, SUSPENDED, SHELVED, ERROR, VERIFY_RESIZE) is a 409.
func liveMigratable(status string) bool {
	return statusIn(status, "ACTIVE", "PAUSED")
}

func coldDrainMode(f *hostDrainVerbFlags) *drainMode {
	mode := &drainMode{
		name:      "cold migration",
		pastTense: "migrated",
		eligible:  coldMigratable,
		whyNot: func(status string) string {
			return whyNotEligible("cold-migrated", status)
		},
		precheck: requireComputeServiceUp,
		action:   coldMigrateBody(f.targetHost),
	}
	if f.confirm {
		mode.pastTense = "migrated and confirmed"
		mode.finish = confirmColdMigration
	}
	return mode
}

// coldMigrateBody builds the migrate action. gophercloud's servers.Migrate
// posts {"migrate": null}, which is right when the scheduler picks the host; a
// target host is a nova 2.56 field and needs the body built directly, as
// runServerMigrate does for the single-server verb.
func coldMigrateBody(targetHost string) map[string]any {
	if targetHost == "" {
		return map[string]any{"migrate": nil}
	}
	return map[string]any{"migrate": map[string]any{"host": targetHost}}
}

// coldMigratable reports whether nova will accept a cold migration for a server
// in this status. nova/compute/api.py decorates resize — cold migration shares
// its code path — with @check_instance_state(vm_state=[vm_states.ACTIVE,
// vm_states.STOPPED]), so ACTIVE and SHUTOFF and nothing else. Note this is a
// different set from live migration's: a stopped server can be cold-migrated
// but not live-migrated, and a paused one the other way round.
func coldMigratable(status string) bool {
	return statusIn(status, "ACTIVE", "SHUTOFF")
}

// requireComputeServiceUp refuses to drain a host nova already considers dead.
//
// It is the mirror of requireComputeServiceDown, and the pair is what makes the
// drain/evacuate split safe to get wrong: each verb refuses the other's case
// and names it, rather than letting the operator discover it as one refusal per
// server. Moving a server needs its own compute service to do the moving —
// nova enforces that outright for a cold migration (compute_api.resize carries
// @check_instance_host(check_is_up=True)) and a live migration cannot get far
// without the source host either.
//
// A service koc cannot read is not an obstacle, as in the evacuate direction:
// an unanswerable check lets the drain proceed and nova decides.
func requireComputeServiceUp(ctx context.Context, client *gophercloud.ServiceClient, host string) error {
	svc, found, ok := computeServiceOn(ctx, client, host)
	if !ok {
		return nil
	}
	if !found {
		return unknownHostError(host)
	}
	if !strings.EqualFold(svc.State, "up") {
		return fmt.Errorf("compute service on host %q is %s; a migration needs it to move the servers.\n"+
			"Use `koc compute host evacuate %s` to rebuild them on other hosts, or bring the service back "+
			"and re-run", host, svc.State, host)
	}
	return nil
}

// confirmColdMigration confirms one landed cold migration.
//
// The server has already left the host by the time this runs, so the remaining
// question is only whether nova is still holding the resize open. A cloud with
// resize_confirm_window set has a periodic task (_poll_unconfirmed_resizes in
// nova/compute/manager.py) confirming these on its own, which makes "somebody
// already confirmed it" an ordinary outcome rather than a failure — both when
// it happened before the status read, and when it happens between that read and
// this POST.
func confirmColdMigration(ctx context.Context, client *gophercloud.ServiceClient, id, status string) (string, error) {
	if !statusIn(status, "VERIFY_RESIZE") {
		return "auto-confirmed by nova (status " + status + ")", nil
	}
	err := servers.ConfirmResize(ctx, client, id).ExtractErr()
	if err == nil {
		return "migrated and confirmed", nil
	}
	if raced, rerr := confirmRaced(ctx, client, id); rerr == nil && raced {
		return "auto-confirmed by nova", nil
	}
	return "", fmt.Errorf("confirming the migration: %w", err)
}

// confirmRaced reports whether a rejected confirmResize means nova's
// resize_confirm_window got there first. Re-reading is what distinguishes that
// from a real refusal: nova answers 409 both for "no longer in VERIFY_RESIZE"
// and for reasons worth surfacing, and only the status says which.
func confirmRaced(ctx context.Context, client *gophercloud.ServiceClient, id string) (bool, error) {
	var s struct {
		Status string `json:"status"`
	}
	if err := servers.Get(ctx, client, id).ExtractInto(&s); err != nil {
		return false, err
	}
	return !statusIn(s.Status, "VERIFY_RESIZE"), nil
}
