package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/services"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	computeapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/compute"
	osresolve "github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// newComputeCommand builds the "compute" parent group, home of
// "compute service ..." and "compute host ...".
func newComputeCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compute",
		Short: "Compute (nova) administrative commands",
	}
	cmd.AddCommand(
		newComputeServiceCommand(a, o),
		newComputeHostCommand(a, o),
	)
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
	status  string
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
	fl.StringVar(&f.status, "status", "", "filter by status: enabled or disabled")
	return cmd
}

func runComputeServiceList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *serviceListFlags, w io.Writer) error {
	// Nova's os-services list has no status query parameter — "status" is a
	// response field, not a filter — so --status is applied after extraction. The
	// valid values are enabled and disabled; anything else is rejected up front
	// rather than silently matching nothing.
	if f.status != "" && f.status != "enabled" && f.status != "disabled" {
		return fmt.Errorf("--status must be enabled or disabled, got %q", f.status)
	}
	// KeyStack's os-services extension adds admin_state and error_details,
	// which gophercloud's Service type drops; ListServices returns them aligned
	// by index.
	all, ext, err := computeapi.ListServices(ctx, client, services.ListOpts{Host: f.host, Binary: f.service})
	if err != nil {
		return err
	}
	// The two slices are aligned by index, so --status has to filter them together.
	if f.status != "" {
		keptServices := make([]services.Service, 0, len(all))
		keptExt := make([]serviceExt, 0, len(ext))
		for i, svc := range all {
			if svc.Status != f.status {
				continue
			}
			keptServices = append(keptServices, svc)
			if i < len(ext) {
				keptExt = append(keptExt, ext[i])
			}
		}
		all, ext = keptServices, keptExt
	}
	return o.WriteList(w, serviceListTable(all, ext, f.long))
}

// serviceExt carries the KeyStack os-services extension fields that gophercloud's
// Service type omits.
type serviceExt = computeapi.ServiceAdminState

// serviceListTable renders the compute-service listing.
//
// Two column groups are conditional rather than gated on --long, because both
// are the read side of a write "compute service set" already performs, and a
// setting you cannot read back is not much of a setting:
//
//   - Disabled Reason appears whenever any listed service carries one. That is
//     exactly when --disable-reason has been used, or when an HA agent or
//     autoevacuator disabled a host and left its reason behind; when nothing is
//     disabled the column would be a blank strip, so it stays out and the
//     vanilla listing is unchanged.
//   - Admin State and Error Details are KeyStack's os-services extension
//     (KCP-1886/7988), which vanilla nova does not return at all — so their
//     presence in the response is itself the signal, and a cloud without the
//     extension sees no change.
//
// --long adds Forced Down, and Disabled Reason unconditionally, so its column
// set does not shift with the state of the fleet.
func serviceListTable(list []services.Service, ext []serviceExt, long bool) output.Table {
	cols := []string{"ID", "Binary", "Host", "Zone", "Status", "State", "Updated At"}
	keystack := slices.ContainsFunc(ext, func(e serviceExt) bool {
		return e.AdminState != "" || e.ErrorDetails != ""
	})
	reasons := long || slices.ContainsFunc(list, func(s services.Service) bool {
		return s.DisabledReason != ""
	})
	if keystack {
		cols = append(cols, "Admin State", "Error Details")
	}
	if reasons {
		cols = append(cols, "Disabled Reason")
	}
	if long {
		cols = append(cols, "Forced Down")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for i, s := range list {
		row := []any{s.ID, s.Binary, s.Host, s.Zone, s.Status, s.State, s.UpdatedAt}
		if keystack {
			var e serviceExt
			if i < len(ext) {
				e = ext[i]
			}
			row = append(row, e.AdminState, e.ErrorDetails)
		}
		if reasons {
			row = append(row, s.DisabledReason)
		}
		if long {
			row = append(row, s.ForcedDown)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
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
		opts := computeapi.ServiceUpdateOpts{
			AdminState:     f.adminState,
			ErrorDetails:   f.errorDetails,
			Status:         services.ServiceStatus(f.status),
			DisabledReason: f.reason,
		}
		if _, err := computeapi.UpdateService(ctx, client, id, opts); err != nil {
			return keystackExtErr(relabel(err, "setting admin state on compute service %s/%s", host, binary), "os-services admin_state")
		}
		if _, err := fmt.Fprintf(w, "Set admin state %s on compute service %s on host %s\n", f.adminState, binary, host); err != nil {
			return err
		}
		return nil
	}

	opts := computeapi.ServiceUpdateOpts{DisabledReason: f.disableReason}
	if f.enable {
		opts.Status = services.ServiceEnabled
	}
	if f.disable {
		opts.Status = services.ServiceDisabled
	}
	if f.up || f.down {
		down := f.down
		opts.ForcedDown = &down
	}
	if body, _ := opts.ToServiceUpdateMap(); len(body) == 0 {
		return fmt.Errorf("nothing to do: pass --enable/--disable and/or --up/--down")
	}

	if _, err := computeapi.UpdateService(ctx, client, id, opts); err != nil {
		return relabel(err, "updating compute service %s/%s", host, binary)
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
	if err := computeapi.DeleteService(ctx, client, id); err != nil {
		return relabel(err, "deleting compute service %s/%s", host, binary)
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
	id, err := computeapi.FindServiceID(ctx, client, host, binary)
	if notFound := (*osresolve.NotFoundError)(nil); errors.As(err, &notFound) {
		return "", fmt.Errorf("no compute service %q found on host %q", binary, host)
	}
	return id, err
}
