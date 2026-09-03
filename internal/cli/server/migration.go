package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// gophercloud v2 has no compute "migrations" package, so "server migration
// list" is implemented against the raw GET /os-migrations endpoint (an
// AGENTS.md-sanctioned raw fallback), decoding into koc-owned DTOs. The
// created-since/created-before filters are a KeyStack extension (KCP-9165 /
// KCP-7192); vanilla nova only understands changes-since/changes-before.

// migration is one entry from GET /os-migrations. Fields track upstream nova:
// uuid appears at microversion 2.59, user_id/project_id at 2.80.
type migration struct {
	ID            int    `json:"id"`
	UUID          string `json:"uuid"`
	SourceNode    string `json:"source_node"`
	DestNode      string `json:"dest_node"`
	SourceCompute string `json:"source_compute"`
	DestCompute   string `json:"dest_compute"`
	DestHost      string `json:"dest_host"`
	Status        string `json:"status"`
	InstanceUUID  string `json:"instance_uuid"`
	// Nova reports the flavor ids as integers (or null); json.Number decodes
	// both safely and renders as the numeric string.
	OldFlavorID   json.Number `json:"old_instance_type_id"`
	NewFlavorID   json.Number `json:"new_instance_type_id"`
	MigrationType string      `json:"migration_type"`
	CreatedAt     string      `json:"created_at"`
	UpdatedAt     string      `json:"updated_at"`
	UserID        string      `json:"user_id"`
	ProjectID     string      `json:"project_id"`
}

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

// migrationListQuery turns the filters into the os-migrations query string,
// resolving --server to the instance UUID nova filters on.
func migrationListQuery(ctx context.Context, client *gophercloud.ServiceClient,
	f *migrationListFlags,
) (url.Values, error) {
	vals := url.Values{}
	for key, val := range map[string]string{
		"host":           f.host,
		"status":         f.status,
		"migration_type": f.migrationType,
		"marker":         f.marker,
		"changes-since":  f.changesSince,
		"changes-before": f.changesBefore,
		"created-since":  f.createdSince,
		"created-before": f.createdBefore,
		"project_id":     f.project,
		"user_id":        f.user,
	} {
		if val != "" {
			vals.Set(key, val)
		}
	}
	if f.server == "" {
		return vals, nil
	}
	id, err := resolveServerID(ctx, client, f.server)
	if err != nil {
		return nil, err
	}
	vals.Set("instance_uuid", id)
	return vals, nil
}

// migrationPage is one os-migrations response page.
type migrationPage struct {
	Migrations []migration `json:"migrations"`
	Links      []struct {
		Href string `json:"href"`
		Rel  string `json:"rel"`
	} `json:"migrations_links"`
}

// nextLink returns the URL of the following page, or "" at the end of the list.
func (p migrationPage) nextLink() string {
	for _, l := range p.Links {
		if l.Rel == "next" {
			return l.Href
		}
	}
	return ""
}

// fetchMigrations follows nova's pagination links from next. Nova treats limit
// only as a page size, so --limit is enforced here as a hard cap on the walk.
func fetchMigrations(ctx context.Context, client *gophercloud.ServiceClient,
	next string, f *migrationListFlags,
) ([]migration, error) {
	var all []migration
	for next != "" {
		var page migrationPage
		resp, err := client.Get(ctx, next, &page, &gophercloud.RequestOpts{OkCodes: []int{200}})
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			if f.createdSince != "" || f.createdBefore != "" {
				return nil, keystackExtErr(fmt.Errorf("listing migrations: %w", err), "created migration-list filters")
			}
			return nil, fmt.Errorf("listing migrations: %w", err)
		}
		all = append(all, page.Migrations...)
		next = page.nextLink()
		if f.limit > 0 && len(all) >= f.limit {
			break
		}
	}
	return all, nil
}

func runServerMigrationList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *migrationListFlags, w io.Writer) error {
	vals, err := migrationListQuery(ctx, client, f)
	if err != nil {
		return err
	}
	next := client.ServiceURL("os-migrations")
	if q := vals.Encode(); q != "" {
		next += "?" + q
	}
	all, err := fetchMigrations(ctx, client, next, f)
	if err != nil {
		return err
	}
	// The walk stops at the cap but a page can overshoot it; trim to exactly
	// --limit.
	if f.limit > 0 && len(all) > f.limit {
		all = all[:f.limit]
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
		var resp struct {
			Migrations []serverMigration `json:"migrations"`
		}
		u := client.ServiceURL("servers", m.InstanceUUID, "migrations")
		r, err := client.Get(ctx, u, &resp, &gophercloud.RequestOpts{OkCodes: []int{200}})
		if r != nil {
			_ = r.Body.Close()
		}
		if err != nil {
			continue
		}
		for _, sm := range resp.Migrations {
			progress[sm.ID] = sm
		}
	}
	return progress
}

// serverMigration is the single migration returned by GET
// /servers/{id}/migrations/{mid} (os-server-migrations, nova 2.23+). Unlike the
// os-migrations list it carries live-migration progress (disk/memory byte
// counters) and names the instance as server_uuid.
type serverMigration struct {
	ID                   int    `json:"id"`
	UUID                 string `json:"uuid"`
	Status               string `json:"status"`
	ServerUUID           string `json:"server_uuid"`
	SourceCompute        string `json:"source_compute"`
	SourceNode           string `json:"source_node"`
	DestCompute          string `json:"dest_compute"`
	DestHost             string `json:"dest_host"`
	DestNode             string `json:"dest_node"`
	MemoryTotalBytes     int64  `json:"memory_total_bytes"`
	MemoryProcessedBytes int64  `json:"memory_processed_bytes"`
	MemoryRemainingBytes int64  `json:"memory_remaining_bytes"`
	DiskTotalBytes       int64  `json:"disk_total_bytes"`
	DiskProcessedBytes   int64  `json:"disk_processed_bytes"`
	DiskRemainingBytes   int64  `json:"disk_remaining_bytes"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
	ProjectID            string `json:"project_id"`
	UserID               string `json:"user_id"`
}

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

// gophercloud v2 has no os-server-migrations package, so "server migration
// show" is a raw GET against /servers/{id}/migrations/{migration_id}, decoding
// into the serverMigration DTO (another AGENTS.md-sanctioned raw fallback).
func runServerMigrationShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, serverRef, migrationID string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, serverRef)
	if err != nil {
		return err
	}
	var resp struct {
		Migration serverMigration `json:"migration"`
	}
	u := client.ServiceURL("servers", id, "migrations", migrationID)
	r, err := client.Get(ctx, u, &resp, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if r != nil {
		_ = r.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("showing migration %s of server %q: %w", migrationID, serverRef, err)
	}
	m := resp.Migration
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
	u := client.ServiceURL("servers", id, "migrations", migrationID)
	r, err := client.Delete(ctx, u, &gophercloud.RequestOpts{OkCodes: []int{202}})
	if r != nil {
		_ = r.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("aborting migration %s of server %q: %w", migrationID, serverRef, err)
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
	u := client.ServiceURL("servers", id, "migrations", migrationID, "action")
	body := map[string]any{"force_complete": nil}
	r, err := client.Post(ctx, u, body, nil, &gophercloud.RequestOpts{OkCodes: []int{202}})
	if r != nil {
		_ = r.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("forcing completion of migration %s of server %q: %w", migrationID, serverRef, err)
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
