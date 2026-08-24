package dns

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The last of the designate surface: the deployment's own limits, and the four
// zone *tasks* that are neither CRUD nor an export/import. None has a gophercloud
// package, so all go through the raw helpers in raw.go:
//
//	dns limit list         GET /v2/limits
//	zone nameservers list  GET /v2/zones/{zone}/nameservers
//	zone abandon           POST /v2/zones/{zone}/tasks/abandon
//	zone axfr              POST /v2/zones/{zone}/tasks/xfr
//	zone move              POST /v2/zones/{zone}/tasks/pool_move
//
// Command and flag names follow upstream python-designateclient 7.0.0. The
// KeyStack command reference at https://docs.keystack.ru/ was not reachable at
// implementation time (HTTP 403), so these are UNVERIFIED against KeyStack and fall
// back to upstream semantics.

// --- dns limit -------------------------------------------------------------

func newDNSLimitCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "limit",
		Short: "Show the DNS limits that apply to this project",
	}
	cmd.AddCommand(newDNSLimitListCommand(a, o))
	return cmd
}

func newDNSLimitListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List DNS limits",
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
			return runDNSLimitList(ctx, client, o, common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

// runDNSLimitList renders /v2/limits as a Limit/Value table, matching upstream. The
// response is a flat object whose keys vary with the designate release, so it is
// decoded generically rather than into a fixed struct — a new limit shows up on its
// own instead of being silently dropped.
func runDNSLimitList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	common *commonOptions, w io.Writer,
) error {
	var limits map[string]any
	if err := dnsGetJSON(ctx, client, client.ServiceURL("limits"), common.headers(), &limits); err != nil {
		return fmt.Errorf("listing DNS limits: %w", err)
	}
	names := make([]string, 0, len(limits))
	for name := range limits {
		names = append(names, name)
	}
	sort.Strings(names)
	t := output.Table{
		Columns: []string{"Limit", "Value"},
		Rows:    make([][]any, 0, len(names)),
	}
	for _, name := range names {
		t.Rows = append(t.Rows, []any{name, limits[name]})
	}
	return o.WriteList(w, t)
}

// --- zone nameservers ------------------------------------------------------

func newZoneNameserversCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nameservers",
		Short: "Show the nameservers serving a zone",
	}
	cmd.AddCommand(newZoneNameserversListCommand(a, o))
	return cmd
}

func newZoneNameserversListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "list <zone>",
		Short: "List the nameservers serving a zone",
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
			return runZoneNameserversList(ctx, client, o, args[0], common, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

func runZoneNameserversList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	zoneRef string, common *commonOptions, w io.Writer,
) error {
	headers := common.headers()
	zoneID, err := resolveZoneID(ctx, withCommonHeaders(client, common), zoneRef)
	if err != nil {
		return err
	}
	var body struct {
		Nameservers []poolNS `json:"nameservers"`
	}
	url := client.ServiceURL("zones", zoneID, "nameservers")
	if err := dnsGetJSON(ctx, client, url, headers, &body); err != nil {
		return fmt.Errorf("listing nameservers of zone %q: %w", zoneRef, err)
	}
	t := output.Table{
		Columns: []string{"Hostname", "Priority"},
		Rows:    make([][]any, 0, len(body.Nameservers)),
	}
	for _, ns := range body.Nameservers {
		t.Rows = append(t.Rows, []any{ns.Hostname, ns.Priority})
	}
	return o.WriteList(w, t)
}

// --- zone tasks ------------------------------------------------------------

// newZoneAbandonCommand builds "zone abandon".
//
// Abandoning removes designate's record of a zone while leaving it in place on the
// nameservers — the opposite trade-off from "zone delete", and an admin-only
// operation because it leaves data behind.
func newZoneAbandonCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "abandon <zone>",
		Short: "Forget a zone without removing it from the nameservers (admin)",
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
			return runZoneTask(ctx, client, args[0],
				zoneTask{task: "abandon", message: "Abandoned zone", common: common}, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

// newZoneAXFRCommand builds "zone axfr": ask designate to re-transfer a secondary
// zone from its masters now rather than at the next refresh.
func newZoneAXFRCommand(a *auth.Options, o *output.Options) *cobra.Command {
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "axfr <zone>",
		Short: "Trigger an immediate AXFR of a secondary zone from its masters",
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
			// The endpoint is spelled "xfr" even though the command is "axfr".
			return runZoneTask(ctx, client, args[0],
				zoneTask{task: "xfr", message: "Scheduled AXFR for zone", common: common}, cmd.OutOrStdout())
		},
	}
	common.bind(cmd)
	return cmd
}

func newZoneMoveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var poolID string
	common := &commonOptions{}
	cmd := &cobra.Command{
		Use:   "move <zone>",
		Short: "Move a zone to another nameserver pool (admin)",
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
			var body map[string]any
			if poolID != "" {
				body = map[string]any{"pool_id": poolID}
			}
			return runZoneTask(ctx, client, args[0],
				zoneTask{task: "pool_move", message: "Scheduled move for zone", body: body, common: common},
				cmd.OutOrStdout())
		},
	}
	// Optional: with no pool designate picks the target itself by scheduler.
	cmd.Flags().StringVar(&poolID, "pool-id", "", "target pool ID (default: let designate choose)")
	common.bind(cmd)
	return cmd
}

// zoneTask names one of the /zones/{zone}/tasks/<task> endpoints and the line
// printed once it is scheduled.
type zoneTask struct {
	task    string
	message string
	body    map[string]any
	common  *commonOptions
}

// runZoneTask posts to one of /zones/{zone}/tasks/<task>. All three tasks are
// fire-and-forget: designate answers 202 with no useful body, so the seam confirms
// what was scheduled rather than rendering a resource.
func runZoneTask(ctx context.Context, client *gophercloud.ServiceClient,
	zoneRef string, t zoneTask, w io.Writer,
) error {
	headers := t.common.headers()
	zoneID, err := resolveZoneID(ctx, withCommonHeaders(client, t.common), zoneRef)
	if err != nil {
		return err
	}
	url := client.ServiceURL("zones", zoneID, "tasks", t.task)
	var jsonBody any
	if t.body != nil {
		jsonBody = t.body
	}
	if err := dnsPostNoContent(ctx, client, url, jsonBody, headers); err != nil {
		return fmt.Errorf("%s %q: %w", t.task, zoneRef, err)
	}
	if _, err := fmt.Fprintf(w, "%s %s\n", t.message, zoneRef); err != nil {
		return err
	}
	return nil
}
