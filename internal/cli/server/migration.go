package server

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	"github.com/ftarasenko/koc/internal/output"
	computeapi "github.com/ftarasenko/koc/pkg/openstack/compute"
)

// The created-since/created-before filters are a KeyStack extension; vanilla
// nova only understands changes-since/changes-before.

// migration is one entry from GET /os-migrations.
type migration = computeapi.Migration

type migrationListFlags struct {
	long          bool
	progress      bool
	host          string
	status        string
	migrationType string
	server        string
	marker        string
	limit         int
	changesSince  string
	changesBefore string
	createdSince  string
	createdBefore string
	project       string
	user          string
}

func newServerMigrationCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "migration", Short: "In-progress and completed server migrations"}
	cmd.AddCommand(
		newServerMigrationListCommand(a, o),
		newServerMigrationShowCommand(a, o),
		newServerMigrationAbortCommand(a, o),
		newServerMigrationForceCommand(a, o),
	)
	return cmd
}

// NewMigrationListCommand is exported so internal/cli can build a second instance
// under the upstream `migration list` spelling (see internal/cli/aliases.go).
func NewMigrationListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newServerMigrationListCommand(a, o)
}

func newServerMigrationListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &migrationListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List server migrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerMigrationList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.StringVar(&f.host, "host", "", "filter by source or destination compute host")
	fl.StringVar(&f.status, "status", "", "filter by migration status")
	fl.StringVar(&f.migrationType, "type", "", "filter by type: evacuation, live-migration, migration (cold), resize")
	fl.StringVar(&f.server, "server", "", "filter by server (name or ID)")
	fl.StringVar(&f.marker, "marker", "", "list migrations after this migration ID (pagination marker)")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of migrations to return")
	fl.StringVar(&f.changesSince, "changes-since", "", "only migrations changed at/after this ISO-8601 time (nova 2.59+)")
	fl.StringVar(&f.changesBefore, "changes-before", "", "only migrations changed at/before this ISO-8601 time (nova 2.66+)")
	// KeyStack migration-list filters (KCP-9165/7192), nova 2.66+; rejected by
	// vanilla nova.
	fl.StringVar(&f.createdSince, "created-since", "", "KeyStack: only migrations created at/after this ISO-8601 time")
	fl.StringVar(&f.createdBefore, "created-before", "", "KeyStack: only migrations created at/before this ISO-8601 time")
	fl.StringVar(&f.project, "project", "", "filter by project ID (nova 2.80+)")
	fl.StringVar(&f.user, "user", "", "filter by user ID (nova 2.80+)")
	// os-migrations carries no progress counters, so --progress costs one extra
	// GET per distinct server with an in-flight migration. Scope the list first
	// (--server, --host, --status) and the cost is one or two calls.
	fl.BoolVar(&f.progress, "progress", false,
		"add the live-migration memory/disk byte counters for in-flight migrations (nova 2.23+)")
	return cmd
}

// migrationListOpts turns the filters into the os-migrations query,
// resolving --server to the instance UUID nova filters on.
func migrationListOpts(ctx context.Context, client *gophercloud.ServiceClient,
	f *migrationListFlags,
) (computeapi.MigrationListOpts, error) {
	opts := computeapi.MigrationListOpts{
		Host:          f.host,
		Status:        f.status,
		MigrationType: f.migrationType,
		Marker:        f.marker,
		ChangesSince:  f.changesSince,
		ChangesBefore: f.changesBefore,
		CreatedSince:  f.createdSince,
		CreatedBefore: f.createdBefore,
		ProjectID:     f.project,
		UserID:        f.user,
	}
	if f.server == "" {
		return opts, nil
	}
	id, err := resolveServerID(ctx, client, f.server)
	if err != nil {
		return opts, err
	}
	opts.InstanceUUID = id
	return opts, nil
}

func runServerMigrationList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *migrationListFlags, w io.Writer) error {
	opts, err := migrationListOpts(ctx, client, f)
	if err != nil {
		return err
	}
	all, err := computeapi.ListMigrations(ctx, client, opts, f.limit)
	if err != nil {
		if f.createdSince != "" || f.createdBefore != "" {
			return keystackExtErr(err, "created migration-list filters")
		}
		return err
	}
	var progress map[int]serverMigration
	if f.progress {
		progress = fetchMigrationProgress(ctx, client, all)
	}
	return o.WriteList(w, migrationTable(all, f.long, progress))
}

// migrationInFlightStatuses are the os-migrations statuses nova reports while a
// migration is still moving data (nova/objects/migration.py). Only these have
// live counters to fetch: /servers/{id}/migrations lists *in-progress* live
// migrations, and a finished one is simply absent from it.
var migrationInFlightStatuses = []string{
	"queued", "preparing", "pre-migrating", "running", "post-migrating",
}

// fetchMigrationProgress collects the live-migration byte counters for the
// listed migrations, keyed by migration ID.
//
// os-migrations does not carry them — memory_total_bytes and friends live only
// on os-server-migrations (GET /servers/{id}/migrations, nova 2.23+) — and that
// endpoint is per-server, so the listed rows are grouped by server and each
// server is asked once. Rows whose status is already terminal are skipped: the
// endpoint would not list them anyway.
//
// Best effort by design: this decorates a listing that is already complete
// without it. A server the caller cannot read, a cloud below 2.23, or a
// migration that finished between the two calls leaves the counters blank
// rather than failing the whole list — which is what an operator watching a
// fault needs, since the row that just completed is the interesting one.
func fetchMigrationProgress(ctx context.Context, client *gophercloud.ServiceClient,
	list []migration,
) map[int]serverMigration {
	progress := map[int]serverMigration{}
	asked := map[string]bool{}
	for _, m := range list {
		if m.InstanceUUID == "" || asked[m.InstanceUUID] {
			continue
		}
		if !slices.Contains(migrationInFlightStatuses, strings.ToLower(m.Status)) {
			continue
		}
		asked[m.InstanceUUID] = true
		list, err := computeapi.ListServerMigrations(ctx, client, m.InstanceUUID)
		if err != nil {
			continue
		}
		for _, sm := range list {
			progress[sm.ID] = sm
		}
	}
	return progress
}

// serverMigration is one migration from os-server-migrations (nova 2.23+),
// with the live-migration progress counters.
type serverMigration = computeapi.ServerMigration

func newServerMigrationShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <server> <migration>",
		Short: "Show an in-progress or completed server migration",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerMigrationShow(ctx, client, o, args[0], args[1], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runServerMigrationShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, serverRef, migrationID string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, serverRef)
	if err != nil {
		return err
	}
	m, err := computeapi.GetServerMigration(ctx, client, id, migrationID)
	if err != nil {
		return relabel(err, "showing migration %s of server %q", migrationID, serverRef)
	}
	fields := []string{
		"ID", "UUID", "Status", "Server UUID",
		"Source Compute", "Source Node", "Dest Compute", "Dest Host", "Dest Node",
		"Memory Total Bytes", "Memory Processed Bytes", "Memory Remaining Bytes",
		"Disk Total Bytes", "Disk Processed Bytes", "Disk Remaining Bytes",
		"Created At", "Updated At", "Project ID", "User ID",
	}
	values := []any{
		m.ID, m.UUID, m.Status, m.ServerUUID,
		m.SourceCompute, m.SourceNode, m.DestCompute, m.DestHost, m.DestNode,
		m.MemoryTotalBytes, m.MemoryProcessedBytes, m.MemoryRemainingBytes,
		m.DiskTotalBytes, m.DiskProcessedBytes, m.DiskRemainingBytes,
		m.CreatedAt, m.UpdatedAt, m.ProjectID, m.UserID,
	}
	return o.WriteSingle(w, fields, values)
}

// newServerMigrationAbortCommand implements "server migration abort <server>
// <migration>" — cancel an in-progress live migration via DELETE
// /servers/{id}/migrations/{migration_id} (os-server-migrations, nova 2.24+).
func newServerMigrationAbortCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "abort <server> <migration>",
		Short: "Abort an in-progress live migration",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerMigrationAbort(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runServerMigrationAbort(ctx context.Context, client *gophercloud.ServiceClient, serverRef, migrationID string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, serverRef)
	if err != nil {
		return err
	}
	if err := relabel(computeapi.AbortServerMigration(ctx, client, id, migrationID),
		"aborting migration %s of server %q", migrationID, serverRef); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Requested abort of migration %s of server %s\n", migrationID, serverRef); err != nil {
		return err
	}
	return nil
}

// newServerMigrationForceCommand models the two-word OSC verb "server migration
// force complete" as a nested "force" parent so cobra resolves it unambiguously
// (mirrors "server console log show").
func newServerMigrationForceCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "force", Short: "Force-control an in-progress migration"}
	cmd.AddCommand(newServerMigrationForceCompleteCommand(a, o))
	return cmd
}

// newServerMigrationForceCompleteCommand implements "server migration force
// complete <server> <migration>" — force an in-progress live migration to
// complete now via POST /servers/{id}/migrations/{migration_id}/action with
// {"force_complete": null} (os-server-migrations, nova 2.22+).
func newServerMigrationForceCompleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "complete <server> <migration>",
		Short: "Force an in-progress live migration to complete",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerMigrationForceComplete(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runServerMigrationForceComplete(ctx context.Context, client *gophercloud.ServiceClient, serverRef, migrationID string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, serverRef)
	if err != nil {
		return err
	}
	if err := relabel(computeapi.ForceCompleteServerMigration(ctx, client, id, migrationID),
		"forcing completion of migration %s of server %q", migrationID, serverRef); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Requested force-complete of migration %s of server %s\n", migrationID, serverRef); err != nil {
		return err
	}
	return nil
}

// migrationProgressColumns are the live-migration counters --progress adds, in
// the order nova reports them. Remaining against total is what answers "did it
// converge, or was it force-completed?": a live migration that converges drives
// memory_remaining_bytes to zero, while one that is force-completed stops with
// it still high.
var migrationProgressColumns = []string{
	"Memory Total Bytes", "Memory Processed Bytes", "Memory Remaining Bytes",
	"Disk Total Bytes", "Disk Processed Bytes", "Disk Remaining Bytes",
}

func migrationTable(list []migration, long bool, progress map[int]serverMigration) output.Table {
	cols := []string{"ID", "UUID", "Source Compute", "Dest Compute", "Server UUID", "Status", "Type", "Created At"}
	if long {
		cols = append(cols, "Source Node", "Dest Node", "Dest Host", "Old Flavor", "New Flavor", "Updated At", "Project ID", "User ID")
	}
	if progress != nil {
		cols = append(cols, migrationProgressColumns...)
	}
	t := output.Table{Columns: cols}
	for _, m := range list {
		row := []any{m.ID, m.UUID, m.SourceCompute, m.DestCompute, m.InstanceUUID, m.Status, m.MigrationType, m.CreatedAt}
		if long {
			row = append(row, m.SourceNode, m.DestNode, m.DestHost, m.OldFlavorID, m.NewFlavorID, m.UpdatedAt, m.ProjectID, m.UserID)
		}
		if progress != nil {
			row = append(row, migrationProgressCells(progress[m.ID])...)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// migrationProgressCells renders one row's counters. A migration with no entry
// in the progress map decodes as the zero value, and nova genuinely reports 0
// for a migration it has not started copying yet — so the two are told apart by
// the ID being unset, and an unknown row is left blank rather than claiming
// zero bytes moved.
func migrationProgressCells(sm serverMigration) []any {
	if sm.ID == 0 {
		return make([]any, len(migrationProgressColumns))
	}
	return []any{
		sm.MemoryTotalBytes, sm.MemoryProcessedBytes, sm.MemoryRemainingBytes,
		sm.DiskTotalBytes, sm.DiskProcessedBytes, sm.DiskRemainingBytes,
	}
}
