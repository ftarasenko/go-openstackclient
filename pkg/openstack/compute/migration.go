package compute

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/gophercloud/gophercloud/v2"
)

// gophercloud v2 has no package for os-migrations or os-server-migrations,
// so these are raw requests decoding into the types below.

// Migration is one entry of GET /os-migrations. uuid appears at
// microversion 2.59, user_id and project_id at 2.80.
type Migration struct {
	ID            int    `json:"id"`
	UUID          string `json:"uuid"`
	SourceNode    string `json:"source_node"`
	DestNode      string `json:"dest_node"`
	SourceCompute string `json:"source_compute"`
	DestCompute   string `json:"dest_compute"`
	DestHost      string `json:"dest_host"`
	Status        string `json:"status"`
	InstanceUUID  string `json:"instance_uuid"`
	// Nova reports the flavor IDs as integers or null.
	OldFlavorID   json.Number `json:"old_instance_type_id"`
	NewFlavorID   json.Number `json:"new_instance_type_id"`
	MigrationType string      `json:"migration_type"`
	CreatedAt     string      `json:"created_at"`
	UpdatedAt     string      `json:"updated_at"`
	UserID        string      `json:"user_id"`
	ProjectID     string      `json:"project_id"`
}

// MigrationListOpts filters [ListMigrations]. Every field is optional.
type MigrationListOpts struct {
	// Host matches the source or destination compute host.
	Host   string
	Status string
	// MigrationType is evacuation, live-migration, migration or resize.
	MigrationType string
	InstanceUUID  string
	// Marker is the migration UUID to list after.
	Marker string
	// ChangesSince needs 2.59, ChangesBefore 2.66 (ISO 8601).
	ChangesSince  string
	ChangesBefore string
	// CreatedSince and CreatedBefore are a KeyStack extension (2.66+);
	// nova rejects them.
	CreatedSince  string
	CreatedBefore string
	// ProjectID and UserID need 2.80.
	ProjectID string
	UserID    string
}

func (o MigrationListOpts) query() url.Values {
	vals := url.Values{}
	for key, val := range map[string]string{
		"host":           o.Host,
		"status":         o.Status,
		"migration_type": o.MigrationType,
		"instance_uuid":  o.InstanceUUID,
		"marker":         o.Marker,
		"changes-since":  o.ChangesSince,
		"changes-before": o.ChangesBefore,
		"created-since":  o.CreatedSince,
		"created-before": o.CreatedBefore,
		"project_id":     o.ProjectID,
		"user_id":        o.UserID,
	} {
		if val != "" {
			vals.Set(key, val)
		}
	}
	return vals
}

type migrationPage struct {
	Migrations []Migration `json:"migrations"`
	Links      []struct {
		Href string `json:"href"`
		Rel  string `json:"rel"`
	} `json:"migrations_links"`
}

func (p migrationPage) next() string {
	for _, l := range p.Links {
		if l.Rel == "next" {
			return l.Href
		}
	}
	return ""
}

// ListMigrations lists migrations, following nova's next links. limit > 0
// caps the result; nova takes limit only as a page size, so it is not sent.
func ListMigrations(ctx context.Context, c *gophercloud.ServiceClient, opts MigrationListOpts, limit int) ([]Migration, error) {
	next := c.ServiceURL("os-migrations")
	if q := opts.query().Encode(); q != "" {
		next += "?" + q
	}
	var all []Migration
	for next != "" {
		var page migrationPage
		if err := getJSON(ctx, c, next, &page); err != nil {
			return nil, fmt.Errorf("listing migrations: %w", err)
		}
		all = append(all, page.Migrations...)
		next = page.next()
		if limit > 0 && len(all) >= limit {
			break
		}
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// ServerMigration is an in-progress live migration from
// os-server-migrations (2.23+). Unlike [Migration] it carries the progress
// counters, and names the instance server_uuid.
type ServerMigration struct {
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

// ListServerMigrations lists a server's in-progress live migrations; a
// finished one is absent.
func ListServerMigrations(ctx context.Context, c *gophercloud.ServiceClient, serverID string) ([]ServerMigration, error) {
	var resp struct {
		Migrations []ServerMigration `json:"migrations"`
	}
	if err := getJSON(ctx, c, c.ServiceURL("servers", serverID, "migrations"), &resp); err != nil {
		return nil, fmt.Errorf("listing migrations of server %s: %w", serverID, err)
	}
	return resp.Migrations, nil
}

// GetServerMigration shows one of a server's migrations.
func GetServerMigration(ctx context.Context, c *gophercloud.ServiceClient, serverID, migrationID string) (*ServerMigration, error) {
	var resp struct {
		Migration ServerMigration `json:"migration"`
	}
	if err := getJSON(ctx, c, c.ServiceURL("servers", serverID, "migrations", migrationID), &resp); err != nil {
		return nil, fmt.Errorf("showing migration %s of server %s: %w", migrationID, serverID, err)
	}
	return &resp.Migration, nil
}

// AbortServerMigration cancels an in-progress live migration (2.24+).
func AbortServerMigration(ctx context.Context, c *gophercloud.ServiceClient, serverID, migrationID string) error {
	r, err := c.Delete(ctx, c.ServiceURL("servers", serverID, "migrations", migrationID),
		&gophercloud.RequestOpts{OkCodes: []int{202}})
	if r != nil {
		_ = r.Body.Close()
	}
	return wrap(err, "aborting migration %s of server %s", migrationID, serverID)
}

// ForceCompleteServerMigration makes an in-progress live migration complete
// now, pausing the guest if it has to (2.22+).
func ForceCompleteServerMigration(ctx context.Context, c *gophercloud.ServiceClient, serverID, migrationID string) error {
	r, err := c.Post(ctx, c.ServiceURL("servers", serverID, "migrations", migrationID, "action"),
		map[string]any{"force_complete": nil}, nil, &gophercloud.RequestOpts{OkCodes: []int{202}})
	if r != nil {
		_ = r.Body.Close()
	}
	return wrap(err, "forcing completion of migration %s of server %s", migrationID, serverID)
}

func getJSON(ctx context.Context, c *gophercloud.ServiceClient, u string, into any) error {
	r, err := c.Get(ctx, u, into, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if r != nil {
		_ = r.Body.Close()
	}
	return err
}
