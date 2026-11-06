package compute

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"

	"github.com/ftarasenko/koc/pkg/openstack/paging"
)

// ServerAction posts a server action body as given, at c's microversion, and
// accepts nova's 200 or 202. It is for the bodies the typed calls here do not
// model, such as a KeyStack extension's.
func ServerAction(ctx context.Context, c *gophercloud.ServiceClient, id string, body map[string]any) error {
	return wrap(serverAction(ctx, c, id, body), "posting an action to server %s", id)
}

// MigrateHostMicroversion is the microversion that added host to a cold
// migration.
const MigrateHostMicroversion = "2.56"

// MigrateServer cold-migrates a server, to host when it is set (needs
// [MigrateHostMicroversion]) and otherwise where the scheduler chooses. Nova
// leaves the server in VERIFY_RESIZE: confirm with [ConfirmResize] or undo
// with [RevertResize].
func MigrateServer(ctx context.Context, c *gophercloud.ServiceClient, id, host string) error {
	var err error
	if host == "" {
		err = servers.Migrate(ctx, c, id).ExtractErr()
	} else {
		err = serverAction(ctx, c, id, map[string]any{"migrate": map[string]any{"host": host}})
	}
	return wrap(err, "migrating server %s", id)
}

// LiveMigrateAutoMicroversion is the microversion that made block_migration
// accept "auto" and removed disk_over_commit.
const LiveMigrateAutoMicroversion = "2.25"

// LiveMigrateOpts is a live migration. gophercloud's servers.LiveMigrateOpts
// cannot send "auto" and drops block_migration when it is unset, which nova
// rejects.
type LiveMigrateOpts struct {
	// Host is the target host; empty lets the scheduler choose.
	Host string
	// BlockMigration forces a block (true) or shared-storage (false)
	// migration. nil means "auto" from [LiveMigrateAutoMicroversion] and false
	// below it.
	BlockMigration *bool
	// DiskOverCommit is sent only below [LiveMigrateAutoMicroversion], where
	// nova still has it.
	DiskOverCommit bool
}

// LiveMigrateBody is the os-migrateLive object opts makes at c's
// microversion. host is always present, as null when unset, because nova
// requires the key.
func LiveMigrateBody(c *gophercloud.ServiceClient, opts LiveMigrateOpts) map[string]any {
	auto := SupportsMicroversion(c, LiveMigrateAutoMicroversion)
	live := map[string]any{}
	switch {
	case opts.BlockMigration != nil:
		live["block_migration"] = *opts.BlockMigration
	case auto:
		live["block_migration"] = "auto"
	default:
		live["block_migration"] = false
	}
	if opts.Host != "" {
		live["host"] = opts.Host
	} else {
		live["host"] = nil
	}
	if !auto {
		live["disk_over_commit"] = opts.DiskOverCommit
	}
	return live
}

// LiveMigrateServer live-migrates a server; it keeps running throughout.
func LiveMigrateServer(ctx context.Context, c *gophercloud.ServiceClient, id string, opts LiveMigrateOpts) error {
	return wrap(serverAction(ctx, c, id, map[string]any{"os-migrateLive": LiveMigrateBody(c, opts)}),
		"live-migrating server %s", id)
}

// EvacuateForceRemovedMicroversion is the microversion that removed force
// from the evacuate action; it was added at 2.29.
const EvacuateForceRemovedMicroversion = "2.68"

// EvacuateOpts is an evacuation. gophercloud's servers.EvacuateOpts always
// sends onSharedStorage, which nova rejects from 2.14.
type EvacuateOpts struct {
	// Host is the target host; empty lets the scheduler choose.
	Host string
	// AdminPass sets the rebuilt server's admin password.
	AdminPass string
	// PreserveEphemeral keeps the ephemeral partition (a KeyStack extension;
	// nova rejects it).
	PreserveEphemeral bool
	// Force skips the scheduler's check of Host. It is dropped from
	// [EvacuateForceRemovedMicroversion] on.
	Force bool
}

// EvacuateBody is the evacuate object opts makes at c's microversion.
func EvacuateBody(c *gophercloud.ServiceClient, opts EvacuateOpts) map[string]any {
	action := map[string]any{}
	if opts.Host != "" {
		action["host"] = opts.Host
	}
	if opts.AdminPass != "" {
		action["adminPass"] = opts.AdminPass
	}
	if opts.PreserveEphemeral {
		action["preserve_ephemeral"] = true
	}
	if opts.Force && !SupportsMicroversion(c, EvacuateForceRemovedMicroversion) {
		action["force"] = true
	}
	return action
}

// EvacuateServer rebuilds a server of a failed host on another host. Nova
// refuses it while the server's compute service is up.
func EvacuateServer(ctx context.Context, c *gophercloud.ServiceClient, id string, opts EvacuateOpts) error {
	return wrap(serverAction(ctx, c, id, map[string]any{"evacuate": EvacuateBody(c, opts)}),
		"evacuating server %s", id)
}

// ConfirmMigration confirms a cold migration or resize, and reports
// autoConfirmed when nova got there first: with resize_confirm_window set,
// nova confirms on its own and then refuses a confirm. A refusal of a server
// still in VERIFY_RESIZE is returned.
func ConfirmMigration(ctx context.Context, c *gophercloud.ServiceClient, id string) (autoConfirmed bool, err error) {
	err = servers.ConfirmResize(ctx, c, id).ExtractErr()
	if err == nil {
		return false, nil
	}
	var s struct {
		Status string `json:"status"`
	}
	if rerr := servers.Get(ctx, c, id).ExtractInto(&s); rerr == nil && !strings.EqualFold(s.Status, "VERIFY_RESIZE") {
		return true, nil
	}
	return false, fmt.Errorf("confirming resize of server %s: %w", id, err)
}

// MigrationFailedError is what the migration waits return when the server
// reaches ERROR.
type MigrationFailedError struct {
	ServerID string
	// Host is the host [WaitForServerToLeaveHost] waited on the server to
	// leave; empty from [WaitForMigration].
	Host string
	// Arrived reports that the server reached its new host in ERROR: the move
	// ran and the rebuild failed.
	Arrived bool
}

func (e *MigrationFailedError) Error() string {
	switch {
	case e.Arrived:
		return fmt.Sprintf("server %s reached its new host in ERROR status", e.ServerID)
	case e.Host != "":
		return fmt.Sprintf("server %s entered ERROR status while leaving host %q", e.ServerID, e.Host)
	default:
		return fmt.Sprintf("server %s entered ERROR status during migration", e.ServerID)
	}
}

// WaitForMigration polls a migrating server every interval until it settles
// with no task running, and returns the status it settled in: ACTIVE or
// PAUSED after a live migration, which keeps the power state, and
// VERIFY_RESIZE after a cold one, which awaits [ConfirmResize]. It gives up
// when ctx is done, after more than five failed reads in a row, and with a
// [*MigrationFailedError] on ERROR.
//
// The task, not the status, says the migration is over: nova keeps ACTIVE
// while it sets a live migration up. Where the source host is known,
// [WaitForServerToLeaveHost] is exact where this is not, since ACTIVE is also
// how a cold migration that nova confirmed on its own ends.
func WaitForMigration(ctx context.Context, c *gophercloud.ServiceClient, id string, interval time.Duration) (string, error) {
	var status string
	err := poll(ctx, c, id, interval,
		func(err error) error { return fmt.Errorf("waiting for migration of server %s: %w", id, err) },
		func(err error) error { return fmt.Errorf("polling server %s during migration: %w", id, err) },
		func(s serverPoll) (bool, error) {
			if strings.EqualFold(s.Status, "ERROR") {
				return false, &MigrationFailedError{ServerID: id}
			}
			if s.TaskState != "" {
				return false, nil
			}
			for _, settled := range []string{"ACTIVE", "PAUSED", "VERIFY_RESIZE"} {
				if strings.EqualFold(s.Status, settled) {
					status = s.Status
					return true, nil
				}
			}
			return false, nil
		})
	return status, err
}

// WaitForServerToLeaveHost polls a moving server every interval until it is
// on a host other than host with no task running, and returns the status it
// settled in. It gives up as [WaitForMigration] does.
//
// The host changes once and in one direction, so this serves a live or cold
// migration and an evacuation alike, where no status does. startStatus is the
// server's status before the move, and decides what ERROR means: a server
// that started in ERROR (an evacuation's usual case) may sit in it on the
// source host, but one that reaches its new host in ERROR, or enters ERROR on
// the way, has failed.
func WaitForServerToLeaveHost(ctx context.Context, c *gophercloud.ServiceClient, id, host, startStatus string, interval time.Duration) (string, error) {
	var status string
	err := poll(ctx, c, id, interval,
		func(err error) error { return fmt.Errorf("waiting for server %s to leave host %q: %w", id, host, err) },
		func(err error) error { return fmt.Errorf("polling server %s during the move: %w", id, err) },
		func(s serverPoll) (bool, error) {
			left := s.TaskState == "" && s.Host != "" && s.Host != host
			if strings.EqualFold(s.Status, "ERROR") {
				switch {
				case left:
					return false, &MigrationFailedError{ServerID: id, Host: host, Arrived: true}
				case !strings.EqualFold(startStatus, "ERROR"):
					return false, &MigrationFailedError{ServerID: id, Host: host}
				}
			}
			status = s.Status
			return left, nil
		})
	return status, err
}

// maxGetErrors is how many consecutive failed reads a migration wait rides out.
const maxGetErrors = 5

type serverPoll struct {
	Status    string `json:"status"`
	TaskState string `json:"OS-EXT-STS:task_state"`
	Host      string `json:"OS-EXT-SRV-ATTR:host"`
}

// poll reads a server every interval until settle reports done or an error.
// A read that fails is retried up to maxGetErrors times in a row.
func poll(ctx context.Context, c *gophercloud.ServiceClient, id string, interval time.Duration,
	timedOut func(error) error, failedRead func(error) error, settle func(serverPoll) (bool, error),
) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var getErrors int
	for {
		var s serverPoll
		if err := servers.Get(ctx, c, id).ExtractInto(&s); err != nil {
			if ctx.Err() != nil {
				return timedOut(ctx.Err())
			}
			getErrors++
			if getErrors > maxGetErrors {
				return failedRead(err)
			}
		} else {
			getErrors = 0
			done, err := settle(s)
			if err != nil || done {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return timedOut(ctx.Err())
		case <-ticker.C:
		}
	}
}

// ListServersOnHost lists every server nova places on host, across all
// projects: an admin call, matched exactly against the compute service host.
// An empty list does not tell an idle host from a misspelt one; ask
// [ListServices] which it is.
func ListServersOnHost(ctx context.Context, c *gophercloud.ServiceClient, host string) ([]servers.Server, error) {
	all, err := paging.Collect(ctx, servers.List(c, servers.ListOpts{Host: host, AllTenants: true}), 0, servers.ExtractServers)
	if err != nil {
		return nil, fmt.Errorf("listing servers on host %q: %w", host, err)
	}
	return all, nil
}
