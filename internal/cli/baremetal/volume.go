package baremetal

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Volume connectors and targets describe a node's boot-from-volume wiring
// (API 1.32, /v1/volume/connectors and /v1/volume/targets). gophercloud has no
// package for them, so the verbs use the raw helpers in raw.go. Ironic accepts
// an update only while the node is powered off, and a delete only then or in a
// state a node may be deleted in.
//
// Flag names follow python-ironicclient (`openstack baremetal volume
// connector|target`). UNVERIFIED against KeyStack (docs.keystack.ru
// unreachable, HTTP 403).

// volumeResource is what the connector and target verbs share: where the
// collection lives and how upstream renders it.
type volumeResource struct {
	kind         string // "volume connector"
	path         string // "volume/connectors"
	key          string // "connectors"
	fields       []string
	detailFields []string
}

var (
	volumeConnector = volumeResource{
		kind: "volume connector", path: "volume/connectors", key: "connectors",
		fields:       []string{"uuid", "node_uuid", "type", "connector_id"},
		detailFields: []string{"uuid", "node_uuid", "type", "connector_id", "extra", "created_at", "updated_at"},
	}
	volumeTarget = volumeResource{
		kind: "volume target", path: "volume/targets", key: "targets",
		fields: []string{"uuid", "node_uuid", "volume_type", "boot_index", "volume_id"},
		detailFields: []string{
			"uuid", "node_uuid", "volume_type", "properties", "boot_index", "extra", "volume_id",
			"created_at", "updated_at",
		},
	}
)

// volumeConnectorTypes are the connector types ironic accepts.
var volumeConnectorTypes = []string{"iqn", "ip", "mac", "wwnn", "wwpn", "port", "portgroup"}

// newVolumeCommand builds "baremetal volume connector|target ...".
func newVolumeCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume",
		Short: "Manage a node's boot-from-volume connectors and targets",
	}
	connector := &cobra.Command{Use: "connector", Short: "Manage baremetal volume connectors"}
	connector.AddCommand(
		newVolumeListCommand(a, o, volumeConnector),
		newVolumeShowCommand(a, o, volumeConnector),
		newVolumeConnectorCreateCommand(a, o),
		newVolumeConnectorSetCommand(a, o),
		newVolumeUnsetCommand(a, o, volumeConnector),
		newVolumeDeleteCommand(a, o, volumeConnector),
	)
	target := &cobra.Command{Use: "target", Short: "Manage baremetal volume targets"}
	target.AddCommand(
		newVolumeListCommand(a, o, volumeTarget),
		newVolumeShowCommand(a, o, volumeTarget),
		newVolumeTargetCreateCommand(a, o),
		newVolumeTargetSetCommand(a, o),
		newVolumeUnsetCommand(a, o, volumeTarget),
		newVolumeDeleteCommand(a, o, volumeTarget),
	)
	cmd.AddCommand(connector, target)
	return cmd
}

type volumeListFlags struct {
	long    bool
	node    string
	limit   int
	marker  string
	sortKey string
	sortDir string
}

func newVolumeListCommand(a *auth.Options, o *output.Options, r volumeResource) *cobra.Command {
	f := &volumeListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List baremetal " + r.kind + "s",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runVolumeList(ctx, client, o, r, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.StringVar(&f.node, "node", "", "limit to "+r.kind+"s of this node (name or UUID)")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of "+r.kind+"s to return")
	fl.StringVar(&f.marker, "marker", "", "UUID of the last "+r.kind+" from the previous page")
	fl.StringVar(&f.sortKey, "sort-key", "", "sort output by this attribute")
	fl.StringVar(&f.sortDir, "sort-dir", "", "sort direction: asc or desc")
	addListFieldsAliases(cmd, o)
	return cmd
}

func runVolumeList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	r volumeResource, f *volumeListFlags, w io.Writer,
) error {
	q := url.Values{"node": {f.node}, "marker": {f.marker}, "sort_key": {f.sortKey}, "sort_dir": {f.sortDir}}
	fields := r.fields
	if f.long || len(o.SelectedColumns(labels(r.detailFields)...)) > len(o.SelectedColumns(labels(r.fields)...)) {
		fields = r.detailFields
		q.Set("detail", "true")
	}
	items, err := ironicList(ctx, client, r.path, r.key, q, f.limit)
	if err != nil {
		return fmt.Errorf("listing baremetal %ss: %w", r.kind, err)
	}
	return o.WriteList(w, listTable(items, fields))
}

func newVolumeShowCommand(a *auth.Options, o *output.Options, r volumeResource) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <" + strings.ReplaceAll(r.kind, " ", "-") + ">",
		Short: "Show details of a baremetal " + r.kind,
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
			return runVolumeShow(ctx, client, o, r, args[0], cmd.OutOrStdout())
		},
	}
	addFieldsAliases(cmd, o)
	return cmd
}

func runVolumeShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	r volumeResource, id string, w io.Writer,
) error {
	var item map[string]any
	if err := ironicGet(ctx, client, joinPath(append(strings.Split(r.path, "/"), id)...), &item); err != nil {
		return fmt.Errorf("getting baremetal %s %s: %w", r.kind, id, err)
	}
	fields, values := showFields(item, r.detailFields)
	return o.WriteSingle(w, fields, values)
}

// volumeCreate posts body and renders the created resource.
func volumeCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	r volumeResource, body map[string]any, extra []string, w io.Writer,
) error {
	if err := o.CheckColumns(knownColumns(r.detailFields)...); err != nil {
		return err
	}
	m, err := parseKeyValMap(extra)
	if err != nil {
		return fmt.Errorf("parsing --extra: %w", err)
	}
	if m != nil {
		body["extra"] = m
	}
	var item map[string]any
	if err := ironicPost(ctx, client, r.path, body, &item); err != nil {
		return fmt.Errorf("creating baremetal %s: %w", r.kind, err)
	}
	fields, values := showFields(item, r.detailFields)
	return o.WriteSingle(w, fields, values)
}

// volumePatch applies ops and renders the updated resource.
func volumePatch(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	r volumeResource, id string, ops patchBuilder, w io.Writer,
) error {
	if err := o.CheckColumns(knownColumns(r.detailFields)...); err != nil {
		return err
	}
	if len(ops) == 0 {
		return fmt.Errorf("%s set/unset requires at least one attribute flag", r.kind)
	}
	var item map[string]any
	if err := ironicPatch(ctx, client, joinPath(append(strings.Split(r.path, "/"), id)...), ops, &item); err != nil {
		return fmt.Errorf("updating baremetal %s %s: %w", r.kind, id, err)
	}
	fields, values := showFields(item, r.detailFields)
	return o.WriteSingle(w, fields, values)
}

// --- connector ---------------------------------------------------------------

type volumeConnectorFlags struct {
	node, connType, connectorID, uuid string
	extra                             []string
}

func (f *volumeConnectorFlags) register(cmd *cobra.Command, verb string) {
	fl := cmd.Flags()
	fl.StringVar(&f.node, "node", "", "node this volume connector belongs to (name or UUID)")
	fl.StringVar(&f.connType, "type", "", "type of the volume connector: "+strings.Join(volumeConnectorTypes, ", "))
	fl.StringVar(&f.connectorID, "connector-id", "",
		"ID of the volume connector in the given type, e.g. an iSCSI initiator IQN")
	fl.StringArrayVar(&f.extra, "extra", nil, verb+" arbitrary metadata key=value (repeatable)")
}

func (f *volumeConnectorFlags) validate() error {
	if f.connType != "" && !slices.Contains(volumeConnectorTypes, f.connType) {
		return fmt.Errorf("--type %q: want one of %s", f.connType, strings.Join(volumeConnectorTypes, ", "))
	}
	return nil
}

func newVolumeConnectorCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &volumeConnectorFlags{}
	cmd := &cobra.Command{
		Use:   "create --node <node> --type <type> --connector-id <id>",
		Short: "Create a new baremetal volume connector",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runVolumeConnectorCreate(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	f.register(cmd, "set")
	cmd.Flags().StringVar(&f.uuid, "uuid", "", "UUID of the volume connector")
	for _, flag := range []string{"node", "type", "connector-id"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}

func runVolumeConnectorCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *volumeConnectorFlags, w io.Writer,
) error {
	if err := f.validate(); err != nil {
		return err
	}
	node, err := nodeUUID(ctx, client, f.node)
	if err != nil {
		return err
	}
	body := map[string]any{"node_uuid": node, "type": f.connType, "connector_id": f.connectorID}
	setIf(body, "uuid", f.uuid)
	return volumeCreate(ctx, client, o, volumeConnector, body, f.extra, w)
}

func newVolumeConnectorSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &volumeConnectorFlags{}
	cmd := &cobra.Command{
		Use:   "set <volume-connector>",
		Short: "Set baremetal volume connector properties (the node must be powered off)",
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
			return runVolumeConnectorSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	f.register(cmd, "set")
	return cmd
}

func runVolumeConnectorSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, f *volumeConnectorFlags, w io.Writer,
) error {
	if err := f.validate(); err != nil {
		return err
	}
	// python-ironicclient patches every attribute with "add", which replaces
	// an existing member.
	var ops patchBuilder
	if f.node != "" {
		node, err := nodeUUID(ctx, client, f.node)
		if err != nil {
			return err
		}
		ops.add("/node_uuid", node)
	}
	if f.connType != "" {
		ops.add("/type", f.connType)
	}
	if f.connectorID != "" {
		ops.add("/connector_id", f.connectorID)
	}
	if err := ops.addKV("/extra", "extra", f.extra); err != nil {
		return err
	}
	return volumePatch(ctx, client, o, volumeConnector, id, ops, w)
}

// --- target ------------------------------------------------------------------

type volumeTargetFlags struct {
	node, volumeType, volumeID, uuid string
	bootIndex                        int
	bootIndexSet                     bool
	properties, extra                []string
}

func (f *volumeTargetFlags) register(cmd *cobra.Command, verb string) {
	fl := cmd.Flags()
	fl.StringVar(&f.node, "node", "", "node this volume target belongs to (name or UUID)")
	fl.StringVar(&f.volumeType, "type", "", "type of the volume target, e.g. iscsi, fibre_channel")
	fl.IntVar(&f.bootIndex, "boot-index", 0, "boot index of the volume target; 0 is the boot volume")
	fl.StringVar(&f.volumeID, "volume-id", "", "ID of the volume (cinder volume UUID)")
	fl.StringArrayVar(&f.properties, "property", nil, verb+" a volume property key=value (repeatable)")
	fl.StringArrayVar(&f.extra, "extra", nil, verb+" arbitrary metadata key=value (repeatable)")
}

func (f *volumeTargetFlags) changed(cmd *cobra.Command) error {
	f.bootIndexSet = cmd.Flags().Changed("boot-index")
	if f.bootIndexSet && f.bootIndex < 0 {
		return fmt.Errorf("expected non-negative --boot-index, got %d", f.bootIndex)
	}
	return nil
}

func newVolumeTargetCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &volumeTargetFlags{}
	cmd := &cobra.Command{
		Use:   "create --node <node> --type <type> --boot-index <index> --volume-id <id>",
		Short: "Create a new baremetal volume target",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if err := f.changed(cmd); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runVolumeTargetCreate(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	f.register(cmd, "set")
	cmd.Flags().StringVar(&f.uuid, "uuid", "", "UUID of the volume target")
	for _, flag := range []string{"node", "type", "boot-index", "volume-id"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}

func runVolumeTargetCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *volumeTargetFlags, w io.Writer,
) error {
	node, err := nodeUUID(ctx, client, f.node)
	if err != nil {
		return err
	}
	body := map[string]any{
		"node_uuid": node, "volume_type": f.volumeType, "boot_index": f.bootIndex, "volume_id": f.volumeID,
	}
	setIf(body, "uuid", f.uuid)
	props, err := parseKeyValMap(f.properties)
	if err != nil {
		return fmt.Errorf("parsing --property: %w", err)
	}
	if props != nil {
		body["properties"] = props
	}
	return volumeCreate(ctx, client, o, volumeTarget, body, f.extra, w)
}

func newVolumeTargetSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &volumeTargetFlags{}
	cmd := &cobra.Command{
		Use:   "set <volume-target>",
		Short: "Set baremetal volume target properties (the node must be powered off)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if err := f.changed(cmd); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runVolumeTargetSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	f.register(cmd, "set")
	return cmd
}

func runVolumeTargetSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, f *volumeTargetFlags, w io.Writer,
) error {
	var ops patchBuilder
	if f.node != "" {
		node, err := nodeUUID(ctx, client, f.node)
		if err != nil {
			return err
		}
		ops.add("/node_uuid", node)
	}
	if f.volumeType != "" {
		ops.add("/volume_type", f.volumeType)
	}
	if f.bootIndexSet {
		ops.add("/boot_index", f.bootIndex)
	}
	if f.volumeID != "" {
		ops.add("/volume_id", f.volumeID)
	}
	if err := ops.addKV("/properties", "property", f.properties); err != nil {
		return err
	}
	if err := ops.addKV("/extra", "extra", f.extra); err != nil {
		return err
	}
	return volumePatch(ctx, client, o, volumeTarget, id, ops, w)
}

// --- unset / delete ----------------------------------------------------------

type volumeUnsetFlags struct {
	extra, properties []string
}

func newVolumeUnsetCommand(a *auth.Options, o *output.Options, r volumeResource) *cobra.Command {
	f := &volumeUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <" + strings.ReplaceAll(r.kind, " ", "-") + ">",
		Short: "Unset baremetal " + r.kind + " properties (the node must be powered off)",
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
			return runVolumeUnset(ctx, client, o, r, args[0], f, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&f.extra, "extra", nil, "extra key to unset (repeatable)")
	if r.key == volumeTarget.key {
		cmd.Flags().StringArrayVar(&f.properties, "property", nil, "property key to unset (repeatable)")
	}
	return cmd
}

func runVolumeUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	r volumeResource, id string, f *volumeUnsetFlags, w io.Writer,
) error {
	var ops patchBuilder
	ops.removeKeys("/extra", f.extra)
	ops.removeKeys("/properties", f.properties)
	return volumePatch(ctx, client, o, r, id, ops, w)
}

func newVolumeDeleteCommand(a *auth.Options, o *output.Options, r volumeResource) *cobra.Command {
	arg := strings.ReplaceAll(r.kind, " ", "-")
	return &cobra.Command{
		Use:   "delete <" + arg + "> [<" + arg + "> ...]",
		Short: "Delete baremetal " + r.kind + "(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runVolumeDelete(ctx, client, r, args, cmd.OutOrStdout())
		},
	}
}

func runVolumeDelete(ctx context.Context, client *gophercloud.ServiceClient, r volumeResource, ids []string, w io.Writer) error {
	return batchdelete.Each(ids, func(id string) error {
		if err := ironicDelete(ctx, client, joinPath(append(strings.Split(r.path, "/"), id)...)); err != nil {
			return fmt.Errorf("deleting baremetal %s %s: %w", r.kind, id, err)
		}
		_, err := fmt.Fprintf(w, "Deleted %s %s\n", r.kind, id)
		return err
	})
}
