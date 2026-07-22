package server

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/services"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newComputeCommand builds the "compute" parent group, home of
// "compute service ...".
func newComputeCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compute",
		Short: "Compute (nova) administrative commands",
	}
	cmd.AddCommand(newComputeServiceCommand(a, o))
	return cmd
}

func newComputeServiceCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage compute (nova) services",
	}
	cmd.AddCommand(
		newComputeServiceListCommand(a, o),
		newComputeServiceSetCommand(a, o),
		newComputeServiceDeleteCommand(a, o),
	)
	return cmd
}

// serviceListFlags holds the filters accepted by "compute service list".
type serviceListFlags struct {
	long    bool
	host    string
	service string
}

func newComputeServiceListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &serviceListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List compute services",
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
			return runComputeServiceList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.StringVar(&f.host, "host", "", "filter by host name")
	// --service filters on the service binary (e.g. nova-compute); -c is handled by output.
	fl.StringVar(&f.service, "service", "", "filter by service binary, e.g. nova-compute")
	return cmd
}

func runComputeServiceList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *serviceListFlags, w io.Writer) error {
	opts := services.ListOpts{Host: f.host, Binary: f.service}
	pages, err := services.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing compute services: %w", err)
	}
	all, err := services.ExtractServices(pages)
	if err != nil {
		return fmt.Errorf("parsing compute service list: %w", err)
	}
	// KeyStack's os-services extension (KCP-1886/7988) adds admin_state and
	// error_details, which gophercloud's Service type drops; pull them raw and
	// align by index (same "services" array, same order).
	ext, err := extractServiceExt(pages)
	if err != nil {
		return fmt.Errorf("parsing compute service list: %w", err)
	}
	return o.WriteList(w, serviceListTable(all, ext, f.long))
}

// serviceExt carries the KeyStack os-services extension fields that gophercloud's
// Service type omits.
type serviceExt struct {
	AdminState   string `json:"admin_state"`
	ErrorDetails string `json:"error_details"`
}

func extractServiceExt(page pagination.Page) ([]serviceExt, error) {
	var s struct {
		Services []serviceExt `json:"services"`
	}
	err := page.(services.ServicePage).ExtractInto(&s)
	return s.Services, err
}

func serviceListTable(list []services.Service, ext []serviceExt, long bool) output.Table {
	cols := []string{"ID", "Binary", "Host", "Zone", "Status", "State", "Updated At"}
	// The KeyStack admin_state/error_details columns are shown only when the
	// cloud actually returns them, so vanilla-nova output stays unchanged.
	keystack := long && slices.ContainsFunc(ext, func(e serviceExt) bool {
		return e.AdminState != "" || e.ErrorDetails != ""
	})
	if long {
		if keystack {
			cols = append(cols, "Admin State", "Error Details")
		}
		cols = append(cols, "Disabled Reason", "Forced Down")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for i, s := range list {
		row := []any{s.ID, s.Binary, s.Host, s.Zone, s.Status, s.State, s.UpdatedAt.String()}
		if long {
			if keystack {
				var e serviceExt
				if i < len(ext) {
					e = ext[i]
				}
				row = append(row, e.AdminState, e.ErrorDetails)
			}
			row = append(row, s.DisabledReason, s.ForcedDown)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// serviceUpdateBody is a raw services.UpdateOptsBuilder. gophercloud's
// services.UpdateOpts tags forced_down with omitempty, so it cannot express
// forced_down=false (needed by "--up"); building the map directly avoids that
// gap while still going through the typed services.Update call.
type serviceUpdateBody map[string]any

func (b serviceUpdateBody) ToServiceUpdateMap() (map[string]any, error) {
	return map[string]any(b), nil
}

// serviceSetFlags holds the mutations accepted by "compute service set".
type serviceSetFlags struct {
	enable        bool
	disable       bool
	disableReason string
	up            bool
	down          bool
	// KeyStack os-services admin_state extension (KCP-1886 / KCP-7988):
	// a single PUT that sets the service's admin_state, optional error_details
	// (for the "Error" state), and optional status/reason. Unknown to vanilla
	// nova, which rejects the body with HTTP 400.
	adminState   string
	errorDetails string
	status       string
	reason       string
}

// keystackAdminStates enumerates the admin_state values KeyStack's os-services
// extension accepts (KCP-1886, plus "Unstable" from KCP-7988). Mirrors the
// admin_state enum in nova's os-services request schema
// (nova/api/openstack/compute/schemas/services.py).
var keystackAdminStates = []string{
	"Active", "EnteringMaintenanceMode", "MaintenanceMode", "Fenced", "Error", "Unstable",
}

func newComputeServiceSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &serviceSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <host> <binary>",
		Short: "Set attributes of a compute service (enable/disable, up/down, KeyStack admin-state)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if f.enable && f.disable {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			if f.up && f.down {
				return fmt.Errorf("--up and --down are mutually exclusive")
			}
			if f.adminState != "" {
				if f.enable || f.disable || f.up || f.down {
					return fmt.Errorf("--admin-state cannot be combined with --enable/--disable/--up/--down")
				}
				if !slices.Contains(keystackAdminStates, f.adminState) {
					return fmt.Errorf("invalid --admin-state %q: want one of %s", f.adminState, strings.Join(keystackAdminStates, ", "))
				}
			}
			if f.errorDetails != "" && f.adminState == "" {
				return fmt.Errorf("--error-details requires --admin-state")
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runComputeServiceSet(ctx, client, args[0], args[1], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.enable, "enable", false, "enable the service")
	fl.BoolVar(&f.disable, "disable", false, "disable the service")
	fl.StringVar(&f.disableReason, "disable-reason", "", "reason for disabling the service")
	fl.BoolVar(&f.up, "up", false, "clear the forced-down flag")
	fl.BoolVar(&f.down, "down", false, "force the service down (fenced by operator)")
	// KeyStack os-services admin_state extension (KCP-1886/7988); vanilla nova
	// rejects the unknown body field with HTTP 400.
	fl.StringVar(&f.adminState, "admin-state", "", "KeyStack: set the service admin state ("+strings.Join(keystackAdminStates, ", ")+")")
	fl.StringVar(&f.errorDetails, "error-details", "", "KeyStack: details for the \"Error\" admin state (requires --admin-state)")
	fl.StringVar(&f.status, "status", "", "KeyStack: enable/disable status to set alongside --admin-state")
	fl.StringVar(&f.reason, "reason", "", "KeyStack: disabled reason to set alongside --admin-state")
	return cmd
}

func runComputeServiceSet(ctx context.Context, client *gophercloud.ServiceClient, host, binary string, f *serviceSetFlags, w io.Writer) error {
	id, err := resolveServiceID(ctx, client, host, binary)
	if err != nil {
		return err
	}

	// KeyStack admin_state path (KCP-1886/KCP-7988): a distinct os-services
	// update carrying admin_state (+ optional error_details/status/reason),
	// mutually exclusive with the standard enable/disable/up/down mutations.
	if f.adminState != "" {
		body := serviceUpdateBody{"admin_state": f.adminState}
		if f.errorDetails != "" {
			body["error_details"] = f.errorDetails
		}
		if f.status != "" {
			body["status"] = f.status
		}
		if f.reason != "" {
			body["disabled_reason"] = f.reason
		}
		if _, err := services.Update(ctx, client, id, body).Extract(); err != nil {
			return keystackExtErr(fmt.Errorf("setting admin state on compute service %s/%s: %w", host, binary, err), "os-services admin_state")
		}
		if _, err := fmt.Fprintf(w, "Set admin state %s on compute service %s on host %s\n", f.adminState, binary, host); err != nil {
			return err
		}
		return nil
	}

	body := serviceUpdateBody{}
	if f.enable {
		body["status"] = string(services.ServiceEnabled)
	}
	if f.disable {
		body["status"] = string(services.ServiceDisabled)
	}
	if f.disableReason != "" {
		body["disabled_reason"] = f.disableReason
	}
	if f.up {
		body["forced_down"] = false
	}
	if f.down {
		body["forced_down"] = true
	}
	if len(body) == 0 {
		return fmt.Errorf("nothing to do: pass --enable/--disable and/or --up/--down")
	}

	if _, err := services.Update(ctx, client, id, body).Extract(); err != nil {
		return fmt.Errorf("updating compute service %s/%s: %w", host, binary, err)
	}
	if _, err := fmt.Fprintf(w, "Updated compute service %s on host %s\n", binary, host); err != nil {
		return err
	}
	return nil
}

func newComputeServiceDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <host> <binary>",
		Short: "Delete (remove) a compute service",
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
			return runComputeServiceDelete(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runComputeServiceDelete(ctx context.Context, client *gophercloud.ServiceClient, host, binary string, w io.Writer) error {
	id, err := resolveServiceID(ctx, client, host, binary)
	if err != nil {
		return err
	}
	if err := services.Delete(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting compute service %s/%s: %w", host, binary, err)
	}
	if _, err := fmt.Fprintf(w, "Deleted compute service %s on host %s\n", binary, host); err != nil {
		return err
	}
	return nil
}

// resolveServiceID looks up a compute service's ID from its host and binary.
// nova's update endpoint keys on the service ID (a UUID since microversion
// 2.53), so operators' host+binary arguments must be resolved first.
func resolveServiceID(ctx context.Context, client *gophercloud.ServiceClient, host, binary string) (string, error) {
	pages, err := services.List(client, services.ListOpts{Host: host, Binary: binary}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving compute service %s/%s: %w", host, binary, err)
	}
	all, err := services.ExtractServices(pages)
	if err != nil {
		return "", fmt.Errorf("resolving compute service %s/%s: %w", host, binary, err)
	}
	for _, s := range all {
		if s.Host == host && s.Binary == binary {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("no compute service %q found on host %q", binary, host)
}
