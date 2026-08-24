package baremetal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Node read-outs and per-node subresources: validate, VIFs, BIOS settings,
// firmware components and inject-NMI.
//
// Verb and flag names mirror upstream python-ironicclient
// (`openstack baremetal node …`). The KeyStack command reference at
// https://docs.keystack.ru/ was not reachable at implementation time (HTTP
// 403), so these are UNVERIFIED against KeyStack and fall back to upstream
// semantics.

// --- validate ---------------------------------------------------------------

func newNodeValidateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "validate <node>",
		Short: "Validate a node's driver interfaces",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeValidate(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

// runNodeValidate renders one row per driver interface, matching upstream's
// Interface/Result/Reason lister. An interface the driver does not implement
// comes back as result=false with an empty reason, which is not the same as a
// misconfiguration — so the reason column is shown verbatim rather than being
// summarised.
func runNodeValidate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	v, err := nodes.Validate(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("validating node %s: %w", id, err)
	}
	ifaces := []struct {
		name string
		res  nodes.DriverValidation
	}{
		{"bios", v.BIOS}, {"boot", v.Boot}, {"console", v.Console}, {"deploy", v.Deploy},
		{"firmware", v.Firmware}, {"inspect", v.Inspect}, {"management", v.Management},
		{"network", v.Network}, {"power", v.Power}, {"raid", v.RAID},
		{"rescue", v.Rescue}, {"storage", v.Storage},
	}
	t := output.Table{
		Columns: []string{"Interface", "Result", "Reason"},
		Rows:    make([][]any, 0, len(ifaces)),
	}
	for _, i := range ifaces {
		t.Rows = append(t.Rows, []any{i.name, i.res.Result, i.res.Reason})
	}
	return o.WriteList(w, t)
}

// --- vif --------------------------------------------------------------------

func newNodeVIFCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vif",
		Short: "Manage the virtual interfaces attached to a node",
	}
	cmd.AddCommand(newNodeVIFListCommand(a, o))
	cmd.AddCommand(newNodeVIFAttachCommand(a, o))
	cmd.AddCommand(newNodeVIFDetachCommand(a, o))
	return cmd
}

func newNodeVIFListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   useListNode,
		Short: "List the virtual interfaces attached to a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeVIFList(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runNodeVIFList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	vifs, err := nodes.ListVirtualInterfaces(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("listing virtual interfaces of node %s: %w", id, err)
	}
	t := output.Table{Columns: []string{"ID"}, Rows: make([][]any, 0, len(vifs))}
	for _, v := range vifs {
		t.Rows = append(t.Rows, []any{v.ID})
	}
	return o.WriteList(w, t)
}

// vifAttachOpts extends gophercloud's VirtualInterfaceOpts with upstream's
// --vif-info, which records arbitrary key/value metadata alongside the VIF. The
// typed struct models only id/port_uuid/portgroup_uuid, so the extra keys are
// merged in here — and never over the three the API defines.
type vifAttachOpts struct {
	nodes.VirtualInterfaceOpts
	Info map[string]any
}

func (o vifAttachOpts) ToVirtualInterfaceMap() (map[string]any, error) {
	body, err := o.VirtualInterfaceOpts.ToVirtualInterfaceMap()
	if err != nil {
		return nil, err
	}
	for k, v := range o.Info {
		if _, reserved := body[k]; reserved {
			return nil, fmt.Errorf("--vif-info may not override %q; use the dedicated flag", k)
		}
		body[k] = v
	}
	return body, nil
}

type vifAttachFlags struct {
	portUUID      string
	portgroupUUID string
	info          []string
}

func newNodeVIFAttachCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &vifAttachFlags{}
	cmd := &cobra.Command{
		Use:   "attach <node> <vif-id>",
		Short: "Attach a virtual interface to a node",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeVIFAttach(ctx, client, args[0], args[1], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.portUUID, "port-uuid", "", "UUID of the baremetal port to attach the VIF to")
	fl.StringVar(&f.portgroupUUID, "portgroup-uuid", "", "UUID of the baremetal port group to attach the VIF to")
	fl.StringArrayVar(&f.info, "vif-info", nil, "extra key=value metadata to record with the VIF (repeatable)")
	cmd.MarkFlagsMutuallyExclusive("port-uuid", "portgroup-uuid")
	return cmd
}

func runNodeVIFAttach(ctx context.Context, client *gophercloud.ServiceClient, id, vifID string,
	f *vifAttachFlags, w io.Writer,
) error {
	extra, err := parseKeyValMap(f.info)
	if err != nil {
		return fmt.Errorf("parsing --vif-info: %w", err)
	}
	opts := vifAttachOpts{
		VirtualInterfaceOpts: nodes.VirtualInterfaceOpts{
			ID:            vifID,
			PortUUID:      f.portUUID,
			PortgroupUUID: f.portgroupUUID,
		},
		Info: extra,
	}
	if err := nodes.AttachVirtualInterface(ctx, client, id, opts).ExtractErr(); err != nil {
		return fmt.Errorf("attaching VIF %s to node %s: %w", vifID, id, err)
	}
	_, err = fmt.Fprintf(w, "Attached VIF %s to node %s\n", vifID, id)
	return err
}

func newNodeVIFDetachCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "detach <node> <vif-id>",
		Short: "Detach a virtual interface from a node",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeVIFDetach(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	}
}

func runNodeVIFDetach(ctx context.Context, client *gophercloud.ServiceClient, id, vifID string, w io.Writer) error {
	if err := nodes.DetachVirtualInterface(ctx, client, id, vifID).ExtractErr(); err != nil {
		return fmt.Errorf("detaching VIF %s from node %s: %w", vifID, id, err)
	}
	_, err := fmt.Fprintf(w, "Detached VIF %s from node %s\n", vifID, id)
	return err
}

// --- bios setting -----------------------------------------------------------

func newNodeBIOSCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bios",
		Short: "Inspect a node's BIOS settings",
	}
	setting := &cobra.Command{
		Use:   "setting",
		Short: "Inspect individual BIOS settings",
	}
	setting.AddCommand(newNodeBIOSSettingListCommand(a, o))
	setting.AddCommand(newNodeBIOSSettingShowCommand(a, o))
	cmd.AddCommand(setting)
	return cmd
}

func newNodeBIOSSettingListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var long bool
	cmd := &cobra.Command{
		Use:   useListNode,
		Short: "List a node's BIOS settings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeBIOSSettingList(ctx, client, o, args[0], long, cmd.OutOrStdout())
		},
	}
	// --long maps to ironic's ?detail=true, which needs API 1.74; below that the
	// extra columns simply come back empty rather than erroring.
	cmd.Flags().BoolVar(&long, "long", false, "show the full setting registry (type, bounds, read-only) — needs ironic API 1.74")
	return cmd
}

func runNodeBIOSSettingList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, long bool, w io.Writer,
) error {
	settings, err := nodes.ListBIOSSettings(ctx, client, id, nodes.ListBIOSSettingsOpts{Detail: long}).Extract()
	if err != nil {
		return fmt.Errorf("listing BIOS settings of node %s: %w", id, err)
	}
	sort.Slice(settings, func(i, j int) bool { return settings[i].Name < settings[j].Name })

	cols := []string{"Name", "Value"}
	if long {
		cols = append(cols, "Attribute Type", "Allowable Values", "Lower Bound", "Upper Bound", "Read Only", "Reset Required", "Unique")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(settings))}
	for _, s := range settings {
		row := []any{s.Name, s.Value}
		if long {
			row = append(row, s.AttributeType, s.AllowableValues,
				derefAny(s.LowerBound), derefAny(s.UpperBound),
				derefAny(s.ReadOnly), derefAny(s.ResetRequired), derefAny(s.Unique))
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// derefAny renders an optional registry field: ironic omits these below API 1.74
// and for settings that do not carry them, and an omitted field must read as
// blank rather than as a zero.
func derefAny[T any](p *T) any {
	if p == nil {
		return ""
	}
	return *p
}

// extractBIOSSetting decodes the single-setting response, replacing
// nodes.GetBIOSSettingResult.Extract.
//
// Ironic keys the object by the setting's own name —
// `{"BootMode": {"name": "BootMode", ...}}`
// (ironic/api/controllers/v1/bios.py get_one: `return {setting_name: ...}`).
// gophercloud v2.13.0 decodes into SingleBIOSSetting{Setting BIOSSetting}, whose
// untagged field matches the literal key "Setting", and its own fixture encodes
// that shape — so against a real cloud Extract silently yields a zero-valued
// setting rather than an error. Delete this and go back to the typed call once
// that is fixed upstream.
//
// The single-entry fallback covers a cloud that keys the object by something
// other than the name the caller asked for (a case-folded name, say).
func extractBIOSSetting(res nodes.GetBIOSSettingResult, name string) (*nodes.BIOSSetting, error) {
	if res.Err != nil {
		return nil, res.Err
	}
	raw, err := json.Marshal(res.Body)
	if err != nil {
		return nil, err
	}
	var byName map[string]nodes.BIOSSetting
	if err := json.Unmarshal(raw, &byName); err != nil {
		return nil, err
	}
	if s, ok := byName[name]; ok {
		return &s, nil
	}
	if len(byName) == 1 {
		for _, s := range byName {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("response contained no setting named %q", name)
}

func newNodeBIOSSettingShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <node> <setting>",
		Short: "Show one BIOS setting of a node",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeBIOSSettingShow(ctx, client, o, args[0], args[1], cmd.OutOrStdout())
		},
	}
}

func runNodeBIOSSettingShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id, name string, w io.Writer,
) error {
	s, err := extractBIOSSetting(nodes.GetBIOSSetting(ctx, client, id, name), name)
	if err != nil {
		return fmt.Errorf("getting BIOS setting %s of node %s: %w", name, id, err)
	}
	fields := []string{
		"name", "value", "attribute_type", "allowable_values", "lower_bound",
		"upper_bound", "min_length", "max_length", "read_only", "reset_required", "unique",
	}
	values := []any{
		s.Name, s.Value, s.AttributeType, s.AllowableValues, derefAny(s.LowerBound),
		derefAny(s.UpperBound), derefAny(s.MinLength), derefAny(s.MaxLength),
		derefAny(s.ReadOnly), derefAny(s.ResetRequired), derefAny(s.Unique),
	}
	return o.WriteSingle(w, fields, values)
}

// --- firmware ---------------------------------------------------------------

func newNodeFirmwareCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "firmware",
		Short: "Inspect a node's firmware components",
	}
	cmd.AddCommand(newNodeFirmwareListCommand(a, o))
	return cmd
}

func newNodeFirmwareListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   useListNode,
		Short: "List a node's firmware components (requires ironic API 1.86)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeFirmwareList(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runNodeFirmwareList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	components, err := nodes.ListFirmware(ctx, client, id).Extract()
	if err != nil {
		// The firmware interface arrived at 1.86, above the Zed cap, so an old
		// cloud has no such route and answers a bare 404 — see microversion.go.
		return explainMicroversion(ctx, client, featureNodeFirmware,
			fmt.Errorf("listing firmware components of node %s: %w", id, err))
	}
	t := output.Table{
		Columns: []string{"Component", "Initial Version", "Current Version", "Last Version Flashed", "Updated At"},
		Rows:    make([][]any, 0, len(components)),
	}
	for _, c := range components {
		var updated any = ""
		if c.UpdatedAt != nil {
			updated = *c.UpdatedAt
		}
		t.Rows = append(t.Rows, []any{c.Component, c.InitialVersion, c.CurrentVersion, c.LastVersionFlashed, updated})
	}
	return o.WriteList(w, t)
}

// --- inject nmi -------------------------------------------------------------

func newNodeInjectNMICommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nmi <node>",
		Short: "Inject a non-masking interrupt into a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runNodeInjectNMI(ctx, client, args[0], cmd.OutOrStdout())
		},
	}
	parent := &cobra.Command{
		Use:   "inject",
		Short: "Inject a signal into a node",
	}
	parent.AddCommand(cmd)
	return parent
}

func runNodeInjectNMI(ctx context.Context, client *gophercloud.ServiceClient, id string, w io.Writer) error {
	if err := nodes.InjectNMI(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("injecting NMI into node %s: %w", id, err)
	}
	_, err := fmt.Fprintf(w, "Injected NMI into node %s\n", id)
	return err
}
