package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Zone exports and imports: designate's asynchronous zonefile tasks. Neither has
// a gophercloud package, so both go through the raw helpers in raw.go.
//
// Command and flag names follow upstream python-designateclient 7.0.0
// (`openstack zone export create|list|show|showfile|delete`,
// `openstack zone import create|list|show|delete`). The KeyStack command
// reference at https://docs.keystack.ru/ was not reachable at implementation time
// (HTTP 403), so these are UNVERIFIED against KeyStack and fall back to upstream
// semantics.

// zoneExport is designate's export-task record.
type zoneExport struct {
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

func zoneExportFields(e *zoneExport) ([]string, []any) {
	return []string{"id", "zone_id", "project_id", "status", "location", "message", "version", "created_at", "updated_at"},
		[]any{e.ID, e.ZoneID, e.ProjectID, e.Status, e.Location, e.Message, e.Version, dnsTimeString(e.CreatedAt), dnsTimeString(e.UpdatedAt)}
}

// zoneImport is designate's import-task record. It carries the same shape as an
// export minus the location, plus the parse error in Message when a zonefile is
// rejected.
type zoneImport struct {
	ID        string `json:"id"`
	ZoneID    string `json:"zone_id"`
	ProjectID string `json:"project_id"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func zoneImportFields(i *zoneImport) ([]string, []any) {
	return []string{"id", "zone_id", "project_id", "status", "message", "version", "created_at", "updated_at"},
		[]any{i.ID, i.ZoneID, i.ProjectID, i.Status, i.Message, i.Version, dnsTimeString(i.CreatedAt), dnsTimeString(i.UpdatedAt)}
}

// --- zone export -----------------------------------------------------------

func newZoneExportCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a zone as a BIND zonefile",
	}
	cmd.AddCommand(
		newZoneExportCreateCommand(a, o),
		newZoneExportListCommand(a, o),
		newZoneExportShowCommand(a, o),
		newZoneExportShowFileCommand(a, o),
		newZoneExportDeleteCommand(a, o),
	)
	return cmd
}

func newZoneExportCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "create <zone>",
		Short: "Start exporting a zone (asynchronous)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneExportCreate(ctx, client, o, args[0], common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

// runZoneExportCreate posts to /zones/{zone}/tasks/export. The response is the
// task record, not the zonefile — "zone export showfile" fetches that once the
// task reaches COMPLETE.
func runZoneExportCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	zoneRef string, common *commonOptions, w io.Writer,
) error {
	headers := common.headers()
	zoneID, err := resolveZoneID(ctx, withCommonHeaders(client, common), zoneRef)
	if err != nil {
		return err
	}
	var export zoneExport
	url := client.ServiceURL("zones", zoneID, "tasks", "export")
	if err := dnsPostJSON(ctx, client, url, nil, &export, headers); err != nil {
		return fmt.Errorf("exporting zone %q: %w", zoneRef, err)
	}
	fields, values := zoneExportFields(&export)
	return o.WriteSingle(w, fields, values)
}

type zoneExportListFlags struct {
	status string
	zoneID string
	limit  int
}

func newZoneExportListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &zoneExportListFlags{}
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List zone exports",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneExportList(ctx, client, o, f, common, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.status, "status", "", "filter by task status, e.g. PENDING or COMPLETE")
	fl.StringVar(&f.zoneID, "zone-id", "", "filter by zone ID")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of exports to return")
	common.bind(cmd)
	return cmd
}

func runZoneExportList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *zoneExportListFlags, common *commonOptions, w io.Writer,
) error {
	q, err := dnsQuery(struct {
		Status string `q:"status"`
		ZoneID string `q:"zone_id"`
	}{f.status, f.zoneID})
	if err != nil {
		return err
	}
	all, err := dnsListAll(ctx, client, client.ServiceURL("zones", "tasks", "exports")+q,
		common.headers(), f.limit,
		func(raw json.RawMessage) ([]zoneExport, string, error) {
			var page struct {
				Exports []zoneExport `json:"exports"`
				Links   dnsLinks     `json:"links"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", fmt.Errorf("parsing zone export list: %w", err)
			}
			return page.Exports, page.Links.Next, nil
		})
	if err != nil {
		return fmt.Errorf("listing zone exports: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Zone ID", "Created At", "Status"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, e := range all {
		t.Rows = append(t.Rows, []any{e.ID, e.ZoneID, dnsTimeString(e.CreatedAt), e.Status})
	}
	return o.WriteList(w, t)
}

func newZoneExportShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "show <export-id>",
		Short: "Show a zone export task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneExportShow(ctx, client, o, args[0], common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

func runZoneExportShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, common *commonOptions, w io.Writer,
) error {
	var export zoneExport
	url := client.ServiceURL("zones", "tasks", "exports", id)
	if err := dnsGetJSON(ctx, client, url, common.headers(), &export); err != nil {
		return fmt.Errorf("showing zone export %s: %w", id, err)
	}
	fields, values := zoneExportFields(&export)
	return o.WriteSingle(w, fields, values)
}

func newZoneExportShowFileCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "showfile <export-id>",
		Short: "Show the zonefile a completed export produced",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneExportShowFile(ctx, client, o, args[0], common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

// runZoneExportShowFile fetches the export's zonefile. The endpoint answers
// text/dns rather than JSON, so the body is carried as a single "data" field —
// the same shape upstream prints, which keeps -f value usable for piping the
// zonefile into a file.
func runZoneExportShowFile(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, common *commonOptions, w io.Writer,
) error {
	url := client.ServiceURL("zones", "tasks", "exports", id, "export")
	zonefile, err := dnsGetText(ctx, client, url, "text/dns", common.headers())
	if err != nil {
		return fmt.Errorf("showing zonefile of export %s: %w", id, err)
	}
	return o.WriteSingle(w, []string{"data"}, []any{zonefile})
}

func newZoneExportDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "delete <export-id> [<export-id>...]",
		Short: "Delete one or more zone export records",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneExportDelete(ctx, client, args, common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

func runZoneExportDelete(ctx context.Context, client *gophercloud.ServiceClient,
	ids []string, common *commonOptions, w io.Writer,
) error {
	headers := common.headers()
	for _, id := range ids {
		url := client.ServiceURL("zones", "tasks", "exports", id)
		if err := dnsDelete(ctx, client, url, headers); err != nil {
			return fmt.Errorf("deleting zone export %s: %w", id, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted zone export %s\n", id); err != nil {
			return err
		}
	}
	return nil
}

// --- zone import -----------------------------------------------------------

func newZoneImportCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a zone from a BIND zonefile",
	}
	cmd.AddCommand(
		newZoneImportCreateCommand(a, o),
		newZoneImportListCommand(a, o),
		newZoneImportShowCommand(a, o),
		newZoneImportDeleteCommand(a, o),
	)
	return cmd
}

func newZoneImportCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var attributes []string
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "create <zonefile>",
		Short: "Import a zone from a zonefile on the local filesystem",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			attrs, err := parseZoneAttributes(attributes)
			if err != nil {
				return err
			}
			zonefile, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("reading zonefile %q: %w", args[0], err)
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneImportCreate(ctx, client, o, string(zonefile), attrs, common, cmd.OutOrStdout())
		},
	}
	// Repeatable <key>:<value>, matching upstream's `--attributes k:v k2:v2`.
	cmd.Flags().StringArrayVar(&attributes, "attributes", nil,
		"zone attribute as <key>:<value> (repeatable)")
	common.bind(cmd)
	return cmd
}

// parseZoneAttributes parses upstream's colon-separated attribute syntax. The
// value may itself contain colons (pool selectors do), so only the first is a
// separator.
func parseZoneAttributes(items []string) (map[string]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	attrs := make(map[string]string, len(items))
	for _, item := range items {
		key, value, found := strings.Cut(item, ":")
		if !found || key == "" {
			return nil, fmt.Errorf("attribute %q is not in <key>:<value> form", item)
		}
		attrs[key] = value
	}
	return attrs, nil
}

// runZoneImportCreate posts the zonefile to /zones/tasks/imports. Designate takes
// the zonefile two ways and the choice is not cosmetic: a bare zonefile goes as
// text/dns, while attributes require a JSON envelope carrying the zonefile as a
// string. Sending the envelope unconditionally would break deployments whose
// designate predates attribute support, so the plain path stays the default.
func runZoneImportCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	zonefile string, attributes map[string]string, common *commonOptions, w io.Writer,
) error {
	var record zoneImport
	url := client.ServiceURL("zones", "tasks", "imports")
	headers := common.headers()
	var err error
	if len(attributes) > 0 {
		body := map[string]any{"zonefile": zonefile, "attributes": attributes}
		err = dnsPostJSON(ctx, client, url, body, &record, headers)
	} else {
		err = dnsPostRaw(ctx, client, url, "text/dns", strings.NewReader(zonefile), &record, headers)
	}
	if err != nil {
		return fmt.Errorf("importing zone: %w", err)
	}
	fields, values := zoneImportFields(&record)
	return o.WriteSingle(w, fields, values)
}

type zoneImportListFlags struct {
	status  string
	zoneID  string
	message string
	limit   int
}

func newZoneImportListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &zoneImportListFlags{}
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List zone imports",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneImportList(ctx, client, o, f, common, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.status, "status", "", "filter by task status, e.g. PENDING or COMPLETE")
	fl.StringVar(&f.zoneID, "zone-id", "", "filter by the zone the import created")
	fl.StringVar(&f.message, "message", "", "filter by task message")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of imports to return")
	common.bind(cmd)
	return cmd
}

func runZoneImportList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *zoneImportListFlags, common *commonOptions, w io.Writer,
) error {
	q, err := dnsQuery(struct {
		Status  string `q:"status"`
		ZoneID  string `q:"zone_id"`
		Message string `q:"message"`
	}{f.status, f.zoneID, f.message})
	if err != nil {
		return err
	}
	all, err := dnsListAll(ctx, client, client.ServiceURL("zones", "tasks", "imports")+q,
		common.headers(), f.limit,
		func(raw json.RawMessage) ([]zoneImport, string, error) {
			var page struct {
				Imports []zoneImport `json:"imports"`
				Links   dnsLinks     `json:"links"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", fmt.Errorf("parsing zone import list: %w", err)
			}
			return page.Imports, page.Links.Next, nil
		})
	if err != nil {
		return fmt.Errorf("listing zone imports: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Zone ID", "Created At", "Status", "Message"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, i := range all {
		t.Rows = append(t.Rows, []any{i.ID, i.ZoneID, dnsTimeString(i.CreatedAt), i.Status, i.Message})
	}
	return o.WriteList(w, t)
}

func newZoneImportShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "show <import-id>",
		Short: "Show a zone import task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneImportShow(ctx, client, o, args[0], common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

func runZoneImportShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, common *commonOptions, w io.Writer,
) error {
	var record zoneImport
	url := client.ServiceURL("zones", "tasks", "imports", id)
	if err := dnsGetJSON(ctx, client, url, common.headers(), &record); err != nil {
		return fmt.Errorf("showing zone import %s: %w", id, err)
	}
	fields, values := zoneImportFields(&record)
	return o.WriteSingle(w, fields, values)
}

func newZoneImportDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "delete <import-id> [<import-id>...]",
		Short: "Delete one or more zone import records",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneImportDelete(ctx, client, args, common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

// runZoneImportDelete removes the import *record*, not the zone it created —
// designate keeps the two lifecycles separate, and so does upstream.
func runZoneImportDelete(ctx context.Context, client *gophercloud.ServiceClient,
	ids []string, common *commonOptions, w io.Writer,
) error {
	headers := common.headers()
	for _, id := range ids {
		url := client.ServiceURL("zones", "tasks", "imports", id)
		if err := dnsDelete(ctx, client, url, headers); err != nil {
			return fmt.Errorf("deleting zone import %s: %w", id, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted zone import %s\n", id); err != nil {
			return err
		}
	}
	return nil
}
