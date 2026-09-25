package network

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/trunks"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Literals repeated within this file.
const (
	flagSegmentationType = "segmentation-type"
	flagSegmentationID   = "segmentation-id"
)

// newTrunkCommand builds "network trunk ...", nested under the "network" noun to
// match upstream's `openstack network trunk ...`.
//
// Flag names follow upstream OSC. UNVERIFIED against KeyStack docs
// (https://docs.keystack.ru/ returned HTTP 403 at implementation time); falls
// back to upstream OSC semantics.
func newTrunkCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trunk",
		Short: "Manage network trunks",
	}
	cmd.AddCommand(
		newTrunkListCommand(a, o),
		newTrunkShowCommand(a, o),
		newTrunkCreateCommand(a, o),
		newTrunkSetCommand(a, o),
		newTrunkUnsetCommand(a, o),
		newTrunkDeleteCommand(a, o),
		newTrunkSubportCommand(a, o),
	)
	return cmd
}

func resolveTrunkID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "network trunk", nameOrID, func(c *gophercloud.ServiceClient) ([]trunks.Trunk, error) {
		pages, err := trunks.List(c, trunks.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return trunks.ExtractTrunks(pages)
	}, func(t trunks.Trunk) string { return t.ID })
}

func trunkFields(t *trunks.Trunk) ([]string, []any) {
	fields := []string{
		"id", "name", "description", "port_id", "status", "admin_state_up",
		"project_id", "sub_ports", "tags", "revision_number", "created_at", "updated_at",
	}
	values := []any{
		t.ID, t.Name, t.Description, t.PortID, t.Status, t.AdminStateUp,
		t.ProjectID, formatSubports(t.Subports), t.Tags, t.RevisionNumber, t.CreatedAt, t.UpdatedAt,
	}
	return fields, values
}

// formatSubports renders the sub-port list compactly as
// "<port-id>:<type>:<segmentation-id>" entries, so a trunk's VLAN mapping is
// readable in a single table cell.
func formatSubports(subports []trunks.Subport) []string {
	out := make([]string, 0, len(subports))
	for _, sp := range subports {
		out = append(out, fmt.Sprintf("%s:%s:%d", sp.PortID, sp.SegmentationType, sp.SegmentationID))
	}
	return out
}

// --- list ------------------------------------------------------------------

type trunkListFlags struct {
	name    string
	port    string
	status  string
	project string
	enable  bool
	disable bool
	long    bool

	adminStateUp *bool
}

func newTrunkListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &trunkListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List network trunks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if err := mutuallyExclusive(fl, "enable", "disable"); err != nil {
				return err
			}
			f.adminStateUp = enableDisable(fl, f.enable, f.disable)
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, f.project, "")
			if err != nil {
				return err
			}
			return runTrunkList(ctx, client, o, f, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter by trunk name")
	fl.StringVar(&f.port, "port", "", "filter by parent port (name or ID)")
	fl.StringVar(&f.status, "status", "", "filter by status (ACTIVE, DOWN, BUILD, DEGRADED, ERROR)")
	fl.StringVar(&f.project, "project", "", "filter by owning project (name or ID)")
	fl.BoolVar(&f.enable, "enable", false, "list only administratively up trunks")
	fl.BoolVar(&f.disable, "disable", false, "list only administratively down trunks")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	return cmd
}

func runTrunkList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *trunkListFlags, projectID string, w io.Writer,
) error {
	opts := trunks.ListOpts{
		Name:         f.name,
		Status:       f.status,
		ProjectID:    projectID,
		AdminStateUp: f.adminStateUp,
	}
	if f.port != "" {
		portID, err := resolvePortID(ctx, client, f.port)
		if err != nil {
			return err
		}
		opts.PortID = portID
	}
	pages, err := trunks.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing network trunks: %w", err)
	}
	all, err := trunks.ExtractTrunks(pages)
	if err != nil {
		return fmt.Errorf("parsing network trunk list: %w", err)
	}

	// --long is upstream's Status/State/Created At/Updated At, then koc's own
	// Sub Ports and Project.
	cols := []string{"ID", "Name", "Parent Port", "Description"}
	if f.long {
		cols = append(cols, "Status", "State", "Created At", "Updated At", "Sub Ports", "Project")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, tr := range all {
		row := []any{tr.ID, tr.Name, tr.PortID, tr.Description}
		if f.long {
			row = append(row, tr.Status, adminState(tr.AdminStateUp), tr.CreatedAt, tr.UpdatedAt,
				formatSubports(tr.Subports), tr.ProjectID)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

// --- show ------------------------------------------------------------------

func newTrunkShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <trunk>",
		Short: "Show details of a network trunk",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runTrunkShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runTrunkShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveTrunkID(ctx, client, ref)
	if err != nil {
		return err
	}
	tr, err := trunks.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing network trunk %q: %w", ref, err)
	}
	fields, values := trunkFields(tr)
	return o.WriteSingle(w, fields, values)
}

// --- create ----------------------------------------------------------------

type trunkCreateFlags struct {
	parentPort    string
	description   string
	subports      []string
	enable        bool
	disable       bool
	project       string
	projectDomain string
	adminStateUp  *bool
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID string
}

func newTrunkCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &trunkCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new network trunk",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if err := mutuallyExclusive(fl, "enable", "disable"); err != nil {
				return err
			}
			f.adminStateUp = enableDisable(fl, f.enable, f.disable)
			if f.parentPort == "" {
				return fmt.Errorf("--parent-port is required: a trunk is a view of one parent port")
			}
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.projectID, err = resolveProjectRef(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			return runTrunkCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.parentPort, "parent-port", "", "parent port of the trunk (name or ID); required")
	fl.StringVar(&f.description, "description", "", "trunk description")
	fl.StringArrayVar(&f.subports, "subport", nil,
		"sub-port to attach: port=<port>[,segmentation-type=<type>][,segmentation-id=<id>] (repeatable)")
	fl.BoolVar(&f.enable, "enable", false, "create the trunk administratively up (the default)")
	fl.BoolVar(&f.disable, "disable", false, "create the trunk administratively down")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID; admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	return cmd
}

func runTrunkCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *trunkCreateFlags, w io.Writer,
) error {
	portID, err := resolvePortID(ctx, client, f.parentPort)
	if err != nil {
		return err
	}
	subports, err := parseSubports(ctx, client, f.subports)
	if err != nil {
		return err
	}
	opts := trunks.CreateOpts{
		Name:         name,
		Description:  f.description,
		PortID:       portID,
		AdminStateUp: f.adminStateUp,
		ProjectID:    f.projectID,
	}
	// Subports go through the body merge: trunks.Subport requires all three
	// fields, and upstream requires only the port.
	var extra map[string]any
	if len(subports) > 0 {
		extra = map[string]any{"sub_ports": subports}
	}
	tr, err := trunks.Create(ctx, client, withTrunkCreateAttrs(opts, extra)).Extract()
	if err != nil {
		return fmt.Errorf("creating network trunk %q: %w", name, err)
	}
	fields, values := trunkFields(tr)
	return o.WriteSingle(w, fields, values)
}

// trunkSubport is one sub-port in a request body. gophercloud's
// trunks.Subport tags all three fields required and never omits them, so it
// cannot express what upstream allows: only the port is required, and the
// segmentation details may be left to neutron (segmentation-type=inherit takes
// no ID; with neither given the server applies its own rules). Zero values are
// omitted — neutron has no segmentation ID 0.
type trunkSubport struct {
	PortID           string `json:"port_id"`
	SegmentationType string `json:"segmentation_type,omitempty"`
	SegmentationID   int    `json:"segmentation_id,omitempty"`
}

// trunkAddSubportsOpts satisfies trunks.AddSubportsOptsBuilder with
// trunkSubport entries.
type trunkAddSubportsOpts []trunkSubport

func (s trunkAddSubportsOpts) ToTrunkAddSubportsMap() (map[string]any, error) {
	return map[string]any{"sub_ports": []trunkSubport(s)}, nil
}

// parseSubports parses the repeatable --subport specs and resolves each port.
// Every spec is parsed before any port is looked up, so a malformed one fails
// without a request. Each spec is a comma-separated key=value list, mirroring
// OSC's --subport: port=<port>[,segmentation-type=<type>][,segmentation-id=<id>].
func parseSubports(ctx context.Context, client *gophercloud.ServiceClient, specs []string) ([]trunkSubport, error) {
	out := make([]trunkSubport, 0, len(specs))
	refs := make([]string, 0, len(specs))
	for _, spec := range specs {
		sp, portRef, err := parseSubportSpec(spec)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
		refs = append(refs, portRef)
	}
	for i, portRef := range refs {
		portID, err := resolvePortID(ctx, client, portRef)
		if err != nil {
			return nil, err
		}
		out[i].PortID = portID
	}
	return out, nil
}

// parseSubportSpec parses one --subport spec. Only port is required, as
// upstream (optional_keys=segmentation-id,segmentation-type). It returns the
// sub-port with everything but PortID filled in, plus the port reference the
// caller resolves to an ID — name resolution needs a client, and keeping it out
// here leaves the key table pure and directly testable, aliases
// (segmentation-id vs segmentation_id) included.
func parseSubportSpec(spec string) (trunkSubport, string, error) {
	var sp trunkSubport
	var portRef string
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, err := splitKV(part)
		if err != nil {
			return sp, "", fmt.Errorf("parsing --subport %q: %w", spec, err)
		}
		switch k {
		case "port":
			portRef = v
		case flagSegmentationType, "segmentation_type":
			sp.SegmentationType = v
		case flagSegmentationID, "segmentation_id":
			id, cerr := strconv.Atoi(strings.TrimSpace(v))
			if cerr != nil {
				return sp, "", fmt.Errorf("parsing --subport %q: segmentation-id %q is not a number", spec, v)
			}
			sp.SegmentationID = id
		default:
			return sp, "", fmt.Errorf("parsing --subport %q: unknown key %q", spec, k)
		}
	}
	if portRef == "" {
		return sp, "", fmt.Errorf("--subport %q requires port", spec)
	}
	return sp, portRef, nil
}

// --- set -------------------------------------------------------------------

type trunkSetFlags struct {
	name        string
	description string
	enable      bool
	disable     bool
	// subports are --subport specs to add after the attribute update, as
	// upstream's set does with add_trunk_subports.
	subports []string

	// nameSet/descSet record which were given: Name and Description are
	// *string in UpdateOpts, so an empty value is a deliberate clear.
	nameSet bool
	descSet bool

	adminStateUp *bool
}

func newTrunkSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &trunkSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <trunk>",
		Short: "Update a network trunk's attributes or add sub-ports",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if err := mutuallyExclusive(fl, "enable", "disable"); err != nil {
				return err
			}
			f.adminStateUp = enableDisable(fl, f.enable, f.disable)
			f.nameSet, f.descSet = fl.Changed("name"), fl.Changed("description")
			if !f.attrsGiven() && len(f.subports) == 0 {
				return fmt.Errorf("nothing to set: pass --name, --description, --enable, --disable or --subport")
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runTrunkSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new trunk name")
	fl.StringVar(&f.description, "description", "", "new trunk description")
	fl.BoolVar(&f.enable, "enable", false, "set the trunk administratively up")
	fl.BoolVar(&f.disable, "disable", false, "set the trunk administratively down")
	fl.StringArrayVar(&f.subports, "subport", nil,
		"sub-port to add: port=<port>[,segmentation-type=<type>][,segmentation-id=<id>] (repeatable)")
	return cmd
}

func (f *trunkSetFlags) attrsGiven() bool {
	return f.nameSet || f.descSet || f.adminStateUp != nil
}

// runTrunkSet sends only the attributes that were actually given, then adds
// any --subport entries. As upstream (whose SDK skips a commit with nothing
// dirty), a subport-only set makes no trunk PUT. Sub-port specs are parsed and
// resolved before either write, so a bad one changes nothing.
func runTrunkSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, f *trunkSetFlags, w io.Writer,
) error {
	id, err := resolveTrunkID(ctx, client, ref)
	if err != nil {
		return err
	}
	subports, err := parseSubports(ctx, client, f.subports)
	if err != nil {
		return err
	}
	var tr *trunks.Trunk
	if f.attrsGiven() {
		if tr, err = trunks.Update(ctx, client, id, trunkUpdateOpts(f)).Extract(); err != nil {
			return fmt.Errorf("updating network trunk %q: %w", ref, err)
		}
	}
	if len(subports) > 0 {
		if tr, err = trunks.AddSubports(ctx, client, id, trunkAddSubportsOpts(subports)).Extract(); err != nil {
			return fmt.Errorf("adding sub-ports to trunk %q: %w", ref, err)
		}
	}
	if tr == nil {
		return fmt.Errorf("nothing to set on trunk %q", ref)
	}
	fields, values := trunkFields(tr)
	return o.WriteSingle(w, fields, values)
}

func trunkUpdateOpts(f *trunkSetFlags) trunks.UpdateOpts {
	opts := trunks.UpdateOpts{AdminStateUp: f.adminStateUp}
	if f.nameSet {
		name := f.name
		opts.Name = &name
	}
	if f.descSet {
		desc := f.description
		opts.Description = &desc
	}
	return opts
}

// --- unset -----------------------------------------------------------------

// newTrunkUnsetCommand builds "network trunk unset --subport <port> <trunk>":
// upstream's older spelling of "trunk subport remove", with --subport required
// and repeatable.
func newTrunkUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var portRefs []string
	cmd := &cobra.Command{
		Use:   "unset <trunk>",
		Short: "Remove sub-ports from a network trunk",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if len(portRefs) == 0 {
				return fmt.Errorf("--subport is required: nothing to unset")
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runTrunkSubportRemove(ctx, client, o, args[0], portRefs, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&portRefs, "subport", nil, "sub-port to remove, by port name or ID (repeatable)")
	return cmd
}

// --- delete ----------------------------------------------------------------

func newTrunkDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <trunk> [<trunk>...]",
		Short: "Delete one or more network trunks",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runTrunkDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runTrunkDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveTrunkID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := trunks.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting network trunk %q: %w", ref, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted network trunk %s\n", ref); err != nil {
			return err
		}
		return nil
	})
}

// --- subport list/add/remove ------------------------------------------------

func newTrunkSubportCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subport",
		Short: "Manage a trunk's sub-ports",
	}
	cmd.AddCommand(
		newTrunkSubportListCommand(a, o),
		newTrunkSubportAddCommand(a, o),
		newTrunkSubportRemoveCommand(a, o),
	)
	return cmd
}

// newSubportCommand builds "network subport list --trunk <trunk>", upstream's
// older spelling of "network trunk subport list" (still registered in OSC
// 10.3.0). Same request, same output.
func newSubportCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var trunk string
	list := &cobra.Command{
		Use:   "list --trunk <trunk>",
		Short: "List a trunk's sub-ports",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if trunk == "" {
				return fmt.Errorf("--trunk is required")
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runTrunkSubportList(ctx, client, o, trunk, cmd.OutOrStdout())
		},
	}
	list.Flags().StringVar(&trunk, "trunk", "", "trunk whose sub-ports to list (name or ID); required")
	cmd := &cobra.Command{
		Use:   "subport",
		Short: "List network trunk sub-ports",
	}
	cmd.AddCommand(list)
	return cmd
}

func newTrunkSubportListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list <trunk>",
		Short: "List a trunk's sub-ports",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runTrunkSubportList(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runTrunkSubportList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveTrunkID(ctx, client, ref)
	if err != nil {
		return err
	}
	subports, err := trunks.GetSubports(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("listing sub-ports of trunk %q: %w", ref, err)
	}
	t := output.Table{
		Columns: []string{"Port", "Segmentation Type", "Segmentation ID"},
		Rows:    make([][]any, 0, len(subports)),
	}
	for _, sp := range subports {
		t.Rows = append(t.Rows, []any{sp.PortID, sp.SegmentationType, sp.SegmentationID})
	}
	return o.WriteList(w, t)
}

type trunkSubportAddFlags struct {
	specs   []string
	segType string
	segID   int
}

// newTrunkSubportAddCommand accepts both shapes: upstream 10.3.0's
// "<trunk> <port> [--segmentation-type T] [--segmentation-id N]" and koc's
// earlier "<trunk> --subport port=…,…" (repeatable, for adding several at
// once). The two are mutually exclusive.
func newTrunkSubportAddCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &trunkSubportAddFlags{}
	cmd := &cobra.Command{
		Use:   "add <trunk> [<port>]",
		Short: "Add sub-ports to a trunk",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			segGiven := fl.Changed(flagSegmentationType) || fl.Changed(flagSegmentationID)
			if err := checkSubportAddShape(len(args) == 2, len(f.specs) > 0, segGiven); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			if len(args) == 2 {
				sp := trunkSubport{SegmentationType: f.segType, SegmentationID: f.segID}
				return runTrunkSubportAddPort(ctx, client, o, args[0], args[1], sp, cmd.OutOrStdout())
			}
			return runTrunkSubportAdd(ctx, client, o, args[0], f.specs, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&f.specs, "subport", nil,
		"sub-port to add: port=<port>[,segmentation-type=<type>][,segmentation-id=<id>] (repeatable; instead of <port>)")
	fl.StringVar(&f.segType, flagSegmentationType, "", "segmentation type of <port>, e.g. vlan")
	fl.IntVar(&f.segID, flagSegmentationID, 0, "segmentation ID of <port>")
	return cmd
}

// checkSubportAddShape rejects a mix of the positional and --subport forms.
func checkSubportAddShape(positional, specs, segGiven bool) error {
	switch {
	case positional && specs:
		return fmt.Errorf("<port> and --subport are mutually exclusive")
	case !positional && !specs:
		return fmt.Errorf("a sub-port is required: pass <port> or --subport")
	case !positional && segGiven:
		return fmt.Errorf("--segmentation-type/--segmentation-id apply to <port>; with --subport put them in the spec")
	}
	return nil
}

// runTrunkSubportAddPort is the positional form: one port, its segmentation
// details already in sp.
func runTrunkSubportAddPort(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref, portRef string, sp trunkSubport, w io.Writer,
) error {
	id, err := resolveTrunkID(ctx, client, ref)
	if err != nil {
		return err
	}
	if sp.PortID, err = resolvePortID(ctx, client, portRef); err != nil {
		return err
	}
	return addTrunkSubports(ctx, client, o, ref, id, []trunkSubport{sp}, w)
}

func addTrunkSubports(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref, id string, subports []trunkSubport, w io.Writer,
) error {
	tr, err := trunks.AddSubports(ctx, client, id, trunkAddSubportsOpts(subports)).Extract()
	if err != nil {
		return fmt.Errorf("adding sub-ports to trunk %q: %w", ref, err)
	}
	fields, values := trunkFields(tr)
	return o.WriteSingle(w, fields, values)
}

func runTrunkSubportAdd(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, specs []string, w io.Writer,
) error {
	id, err := resolveTrunkID(ctx, client, ref)
	if err != nil {
		return err
	}
	subports, err := parseSubports(ctx, client, specs)
	if err != nil {
		return err
	}
	return addTrunkSubports(ctx, client, o, ref, id, subports, w)
}

// newTrunkSubportRemoveCommand accepts upstream 10.3.0's "<trunk> <port>..."
// and koc's earlier "<trunk> --subport <port>" (repeatable); not both at once.
func newTrunkSubportRemoveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var portRefs []string
	cmd := &cobra.Command{
		Use:   "remove <trunk> [<port>...]",
		Short: "Remove sub-ports from a trunk",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			refs := portRefs
			switch {
			case len(args) > 1 && len(portRefs) > 0:
				return fmt.Errorf("<port> and --subport are mutually exclusive")
			case len(args) > 1:
				refs = args[1:]
			case len(portRefs) == 0:
				return fmt.Errorf("a sub-port is required: pass <port> or --subport")
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runTrunkSubportRemove(ctx, client, o, args[0], refs, cmd.OutOrStdout())
		},
	}
	// Removal keys on the port alone — the segmentation details are not part of
	// the request — so this takes a bare port reference, not a key=value spec.
	cmd.Flags().StringArrayVar(&portRefs, "subport", nil, "sub-port to remove, by port name or ID (repeatable; instead of <port>)")
	return cmd
}

func runTrunkSubportRemove(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, portRefs []string, w io.Writer,
) error {
	id, err := resolveTrunkID(ctx, client, ref)
	if err != nil {
		return err
	}
	remove := make([]trunks.RemoveSubport, 0, len(portRefs))
	for _, portRef := range portRefs {
		portID, rerr := resolvePortID(ctx, client, portRef)
		if rerr != nil {
			return rerr
		}
		remove = append(remove, trunks.RemoveSubport{PortID: portID})
	}
	tr, err := trunks.RemoveSubports(ctx, client, id, trunks.RemoveSubportsOpts{Subports: remove}).Extract()
	if err != nil {
		return fmt.Errorf("removing sub-ports from trunk %q: %w", ref, err)
	}
	fields, values := trunkFields(tr)
	return o.WriteSingle(w, fields, values)
}
