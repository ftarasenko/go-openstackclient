package keyvrm

import (
	"context"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func newAppConfigCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "app-config", Short: "Manage the KeyVRM application configuration"}
	cmd.AddCommand(newAppConfigShow(a, o), newAppConfigSet(a, o))
	return cmd
}

func newAppConfigShow(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the KeyVRM application configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runAppConfigShow(cmd.Context(), sc, o, cmd.OutOrStdout())
		},
	}
}

func runAppConfigShow(ctx context.Context, sc *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	cfg, err := getAppConfig(ctx, sc)
	if err != nil {
		return err
	}
	fields, values := appConfigView(cfg)
	return o.WriteSingle(w, fields, values)
}

// appConfigSetSpec maps each app-config "set" flag to its JSON body key and type.
// Only explicitly-set flags are sent (exclude_none semantics).
var appConfigSetSpec = []flagSpec{
	{"enabled", "enabled", kindBool},
	{"period", "period", kindInt},
	{"nova-enabled-filters", "nova_enabled_filters", kindStr},
	{"ha-preserve-ephemeral-device", "ha_preserve_ephemeral_device", kindBool},
	{"ha-evacuate-order-key", "ha_evacuate_order_key", kindStr},
	{"ha-no-evacuate-key", "ha_no_evacuate_key", kindStr},
	{"ha-vm-state-reset-timeout", "ha_vm_state_reset_timeout", kindInt},
	{"ha-fence-failed-interfaces", "ha_fence_failed_interfaces", kindStrSlice},
	{"ha-fence-ceph", "ha_fence_ceph", kindBool},
	{"ha-fence-bmc", "ha_fence_bmc", kindBool},
	{"ha-fence-nova", "ha_fence_nova", kindBool},
	{"ha-check-failed-interfaces", "ha_check_failed_interfaces", kindStrSlice},
	{"ha-bond-names", "ha_bond_names", kindStrSlice},
	{"ha-power-fence-mode", "ha_power_fence_mode", kindStr},
	{"ha-power-check-timeout", "ha_power_check_timeout", kindInt},
	{"lb-no-migrate-key", "lb_no_migrate_key", kindStr},
	{"executor-timeout", "executor_timeout", kindInt},
	{"executor-max-attempts", "executor_max_attempts", kindInt},
	{"executor-max-repeated-errors", "executor_max_repeated_errors", kindInt},
	{"executor-manual-action-timeout", "executor_manual_action_timeout", kindInt},
}

func newAppConfigSet(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Update the KeyVRM application configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			body := buildBody(cmd, appConfigSetSpec)
			if len(body) == 0 {
				return errNoUpdateFields
			}
			sc, err := newKeyVRMClient(cmd.Context(), a)
			if err != nil {
				return err
			}
			return runAppConfigSet(cmd.Context(), sc, o, body, cmd.OutOrStdout())
		},
	}
	fs := cmd.Flags()
	fs.Bool("enabled", false, "enable/disable the service")
	fs.Int("period", 0, "period in seconds")
	fs.String("nova-enabled-filters", "", "nova enabled filters")
	fs.Bool("ha-preserve-ephemeral-device", false, "preserve ephemeral device")
	fs.String("ha-evacuate-order-key", "", "evacuate order key")
	fs.String("ha-no-evacuate-key", "", "no-evacuate key")
	fs.Int("ha-vm-state-reset-timeout", 0, "VM state reset timeout")
	fs.StringArray("ha-fence-failed-interfaces", nil, "fence failed interfaces (repeatable)")
	fs.Bool("ha-fence-ceph", false, "enable Ceph fencing")
	fs.Bool("ha-fence-bmc", false, "enable BMC fencing")
	fs.Bool("ha-fence-nova", false, "enable Nova fencing")
	fs.StringArray("ha-check-failed-interfaces", nil, "check failed interfaces (repeatable)")
	fs.StringArray("ha-bond-names", nil, "bond names (repeatable)")
	fs.String("ha-power-fence-mode", "", "power fence mode")
	fs.Int("ha-power-check-timeout", 0, "power check timeout")
	fs.String("lb-no-migrate-key", "", "load-balancer no-migrate key")
	fs.Int("executor-timeout", 0, "executor timeout")
	fs.Int("executor-max-attempts", 0, "executor max attempts")
	fs.Int("executor-max-repeated-errors", 0, "executor max repeated errors")
	fs.Int("executor-manual-action-timeout", 0, "executor manual action timeout")
	return cmd
}

func runAppConfigSet(ctx context.Context, sc *gophercloud.ServiceClient, o *output.Options, body map[string]any, w io.Writer) error {
	cfg, err := updateAppConfig(ctx, sc, body)
	if err != nil {
		return err
	}
	fields, values := appConfigView(cfg)
	return o.WriteSingle(w, fields, values)
}
