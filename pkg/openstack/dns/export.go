package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
)

// ZoneExport is a zone export task.
type ZoneExport struct {
	ID        string `json:"id"`
	ZoneID    string `json:"zone_id"`
	ProjectID string `json:"project_id"`
	Status    string `json:"status"`
	Location  string `json:"location"`
	Message   string `json:"message"`
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ZoneImport is a zone import task.
type ZoneImport struct {
	ID        string `json:"id"`
	ZoneID    string `json:"zone_id"`
	ProjectID string `json:"project_id"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ZoneExportListOpts filters ListZoneExports.
type ZoneExportListOpts struct {
	Status string `q:"status"`
	ZoneID string `q:"zone_id"`
}

// ZoneImportListOpts filters ListZoneImports.
type ZoneImportListOpts struct {
	Status  string `q:"status"`
	ZoneID  string `q:"zone_id"`
	Message string `q:"message"`
}

// CreateZoneExport starts exporting a zone to a zonefile.
func CreateZoneExport(ctx context.Context, c *gophercloud.ServiceClient, zoneID string) (*ZoneExport, error) {
	var e ZoneExport
	if err := postJSON(ctx, c, c.ServiceURL("zones", zoneID, "tasks", "export"), nil, &e); err != nil {
		return nil, wrap(err, "exporting zone %s", zoneID)
	}
	return &e, nil
}

// ListZoneExports lists zone exports, stopping after limit results (0 = no
// cap).
func ListZoneExports(ctx context.Context, c *gophercloud.ServiceClient, opts ZoneExportListOpts, limit int) ([]ZoneExport, error) {
	q, err := query(opts)
	if err != nil {
		return nil, err
	}
	all, err := listAll(ctx, c, c.ServiceURL("zones", "tasks", "exports")+q, limit,
		func(raw json.RawMessage) ([]ZoneExport, string, error) {
			var page struct {
				Exports []ZoneExport `json:"exports"`
				Links   links        `json:"links"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", fmt.Errorf("parsing zone export list: %w", err)
			}
			return page.Exports, page.Links.Next, nil
		})
	return all, wrap(err, "listing zone exports")
}

// GetZoneExport reads one zone export.
func GetZoneExport(ctx context.Context, c *gophercloud.ServiceClient, id string) (*ZoneExport, error) {
	var e ZoneExport
	if err := getJSON(ctx, c, c.ServiceURL("zones", "tasks", "exports", id), &e); err != nil {
		return nil, wrap(err, "showing zone export %s", id)
	}
	return &e, nil
}

// GetZoneExportFile reads a finished export's zonefile (text/dns).
func GetZoneExportFile(ctx context.Context, c *gophercloud.ServiceClient, id string) (string, error) {
	zonefile, err := getText(ctx, c, c.ServiceURL("zones", "tasks", "exports", id, "export"), "text/dns")
	return zonefile, wrap(err, "showing zonefile of export %s", id)
}

// DeleteZoneExport deletes a zone export.
func DeleteZoneExport(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(del(ctx, c, c.ServiceURL("zones", "tasks", "exports", id)), "deleting zone export %s", id)
}

// CreateZoneImport imports a zonefile. With attributes it sends a JSON body;
// without, the zonefile itself as text/dns.
func CreateZoneImport(ctx context.Context, c *gophercloud.ServiceClient, zonefile string,
	attributes map[string]string,
) (*ZoneImport, error) {
	var record ZoneImport
	url := c.ServiceURL("zones", "tasks", "imports")
	var err error
	if len(attributes) > 0 {
		body := map[string]any{"zonefile": zonefile, "attributes": attributes}
		err = postJSON(ctx, c, url, body, &record)
	} else {
		err = postRaw(ctx, c, url, "text/dns", strings.NewReader(zonefile), &record)
	}
	if err != nil {
		return nil, wrap(err, "importing zone")
	}
	return &record, nil
}

// ListZoneImports lists zone imports, stopping after limit results (0 = no
// cap).
func ListZoneImports(ctx context.Context, c *gophercloud.ServiceClient, opts ZoneImportListOpts, limit int) ([]ZoneImport, error) {
	q, err := query(opts)
	if err != nil {
		return nil, err
	}
	all, err := listAll(ctx, c, c.ServiceURL("zones", "tasks", "imports")+q, limit,
		func(raw json.RawMessage) ([]ZoneImport, string, error) {
			var page struct {
				Imports []ZoneImport `json:"imports"`
				Links   links        `json:"links"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", fmt.Errorf("parsing zone import list: %w", err)
			}
			return page.Imports, page.Links.Next, nil
		})
	return all, wrap(err, "listing zone imports")
}

// GetZoneImport reads one zone import.
func GetZoneImport(ctx context.Context, c *gophercloud.ServiceClient, id string) (*ZoneImport, error) {
	var record ZoneImport
	if err := getJSON(ctx, c, c.ServiceURL("zones", "tasks", "imports", id), &record); err != nil {
		return nil, wrap(err, "showing zone import %s", id)
	}
	return &record, nil
}

// DeleteZoneImport deletes a zone import.
func DeleteZoneImport(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(del(ctx, c, c.ServiceURL("zones", "tasks", "imports", id)), "deleting zone import %s", id)
}
