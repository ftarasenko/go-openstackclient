package baremetal

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newNodeShowCommand builds "baremetal node show <node>".
func newNodeShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <node>",
		Short: "Show details of a baremetal node",
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
			return runNodeShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runNodeShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	n, err := nodes.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting baremetal node %s: %w", id, err)
	}
	fields, values := nodeShowFields(n)
	return o.WriteSingle(w, fields, values)
}

// nodeCreateFlags holds the attributes accepted by "node create".
//
// Flag names follow upstream OSC (`openstack baremetal node create`). The
// KeyStack command reference at https://docs.keystack.ru/ was not reachable at
// implementation time (HTTP 403), so these are UNVERIFIED against KeyStack and
// fall back to upstream OSC semantics.
type nodeCreateFlags struct {
	name           string
	driver         string
	resourceClass  string
	uuid           string
	conductorGroup string
	property       []string
	driverInfo     []string
	extra          []string
}

func newNodeCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &nodeCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new baremetal node",
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
			return runNodeCreate(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "unique name for the node")
	fl.StringVar(&f.driver, "driver", "", "driver used to manage the node (required)")
	fl.StringVar(&f.resourceClass, "resource-class", "", "resource class for scheduling")
	fl.StringVar(&f.uuid, "uuid", "", "UUID to assign to the node")
	fl.StringVar(&f.conductorGroup, "conductor-group", "", "conductor group for the node")
	fl.StringArrayVar(&f.property, "property", nil, "physical property key=value (repeatable)")
	fl.StringArrayVar(&f.driverInfo, "driver-info", nil, "driver_info key=value (repeatable)")
	fl.StringArrayVar(&f.extra, "extra", nil, "arbitrary metadata key=value (repeatable)")
	_ = cmd.MarkFlagRequired("driver")
	return cmd
}

func runNodeCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *nodeCreateFlags, w io.Writer) error {
	props, err := parseKeyValMap(f.property)
	if err != nil {
		return fmt.Errorf("parsing --property: %w", err)
	}
	dinfo, err := parseKeyValMap(f.driverInfo)
	if err != nil {
		return fmt.Errorf("parsing --driver-info: %w", err)
	}
	extra, err := parseKeyValMap(f.extra)
	if err != nil {
		return fmt.Errorf("parsing --extra: %w", err)
	}
	opts := nodes.CreateOpts{
		Name:           f.name,
		Driver:         f.driver,
		ResourceClass:  f.resourceClass,
		UUID:           f.uuid,
		ConductorGroup: f.conductorGroup,
		Properties:     props,
		DriverInfo:     dinfo,
		Extra:          extra,
	}
	n, err := nodes.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating baremetal node: %w", err)
	}
	fields, values := nodeShowFields(n)
	return o.WriteSingle(w, fields, values)
}

func newNodeDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <node> [<node> ...]",
		Short: "Delete baremetal node(s)",
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
			return runNodeDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runNodeDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string, w io.Writer) error {
	// Attempt every id (as OSC does) rather than aborting on the first failure;
	// report the successes and join the failures into a single error.
	var errs []error
	for _, id := range ids {
		if err := nodes.Delete(ctx, client, id).ExtractErr(); err != nil {
			errs = append(errs, fmt.Errorf("deleting baremetal node %s: %w", id, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Deleted node %s\n", id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// nodeSetFlags holds the mutable attributes accepted by "node set".
//
// Flag names follow upstream OSC (`openstack baremetal node set`). The KeyStack
// command reference at https://docs.keystack.ru/ was not reachable at
// implementation time (HTTP 403), so these are UNVERIFIED against KeyStack and
// fall back to upstream OSC semantics.
type nodeSetFlags struct {
	name           string
	driver         string
	resourceClass  string
	conductorGroup string
	instanceUUID   string
	property       []string
	driverInfo     []string
	extra          []string
}

func newNodeSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &nodeSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <node>",
		Short: "Set baremetal node properties",
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
			return runNodeSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "set the node name")
	fl.StringVar(&f.driver, "driver", "", "set the node driver")
	fl.StringVar(&f.resourceClass, "resource-class", "", "set the resource class")
	fl.StringVar(&f.conductorGroup, "conductor-group", "", "set the conductor group")
	fl.StringVar(&f.instanceUUID, "instance-uuid", "", "associate the node with this instance UUID")
	fl.StringArrayVar(&f.property, "property", nil, "set a physical property key=value (repeatable)")
	fl.StringArrayVar(&f.driverInfo, "driver-info", nil, "set a driver_info key=value (repeatable)")
	fl.StringArrayVar(&f.extra, "extra", nil, "set an extra metadata key=value (repeatable)")
	return cmd
}

func runNodeSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, f *nodeSetFlags, w io.Writer) error {
	var ops nodes.UpdateOpts
	scalar := func(_, path, val string) {
		ops = append(ops, nodes.UpdateOperation{Op: nodes.ReplaceOp, Path: path, Value: val})
	}
	if f.name != "" {
		scalar("name", "/name", f.name)
	}
	if f.driver != "" {
		scalar("driver", "/driver", f.driver)
	}
	if f.resourceClass != "" {
		scalar("resource-class", "/resource_class", f.resourceClass)
	}
	if f.conductorGroup != "" {
		scalar("conductor-group", "/conductor_group", f.conductorGroup)
	}
	if f.instanceUUID != "" {
		scalar("instance-uuid", "/instance_uuid", f.instanceUUID)
	}
	if err := appendKVOps(&ops, "/properties/", f.property, "--property"); err != nil {
		return err
	}
	if err := appendKVOps(&ops, "/driver_info/", f.driverInfo, "--driver-info"); err != nil {
		return err
	}
	if err := appendKVOps(&ops, "/extra/", f.extra, "--extra"); err != nil {
		return err
	}
	if len(ops) == 0 {
		return fmt.Errorf("node set requires at least one attribute flag")
	}
	n, err := nodes.Update(ctx, client, id, ops).Extract()
	if err != nil {
		return fmt.Errorf("updating baremetal node %s: %w", id, err)
	}
	fields, values := nodeShowFields(n)
	return o.WriteSingle(w, fields, values)
}

// appendKVOps adds JSON-patch "add" operations for a set of key=value pairs at a
// given path prefix (e.g. "/properties/").
func appendKVOps(ops *nodes.UpdateOpts, prefix string, pairs []string, flag string) error {
	for _, p := range pairs {
		k, v, err := parseKeyVal(p)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", flag, err)
		}
		*ops = append(*ops, nodes.UpdateOperation{Op: nodes.AddOp, Path: prefix + escapeJSONPointer(k), Value: v})
	}
	return nil
}

// nodeUnsetFlags holds the attributes removable by "node unset".
type nodeUnsetFlags struct {
	name          bool
	resourceClass bool
	instanceUUID  bool
	property      []string
	driverInfo    []string
	extra         []string
}

func newNodeUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &nodeUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <node>",
		Short: "Unset baremetal node properties",
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
			return runNodeUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.name, "name", false, "clear the node name")
	fl.BoolVar(&f.resourceClass, "resource-class", false, "clear the resource class")
	fl.BoolVar(&f.instanceUUID, "instance-uuid", false, "disassociate the node from its instance")
	fl.StringArrayVar(&f.property, "property", nil, "remove a physical property by key (repeatable)")
	fl.StringArrayVar(&f.driverInfo, "driver-info", nil, "remove a driver_info key (repeatable)")
	fl.StringArrayVar(&f.extra, "extra", nil, "remove an extra metadata key (repeatable)")
	return cmd
}

func runNodeUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, f *nodeUnsetFlags, w io.Writer) error {
	var ops nodes.UpdateOpts
	remove := func(path string) {
		ops = append(ops, nodes.UpdateOperation{Op: nodes.RemoveOp, Path: path})
	}
	if f.name {
		remove("/name")
	}
	if f.resourceClass {
		remove("/resource_class")
	}
	if f.instanceUUID {
		remove("/instance_uuid")
	}
	for _, k := range f.property {
		remove("/properties/" + escapeJSONPointer(k))
	}
	for _, k := range f.driverInfo {
		remove("/driver_info/" + escapeJSONPointer(k))
	}
	for _, k := range f.extra {
		remove("/extra/" + escapeJSONPointer(k))
	}
	if len(ops) == 0 {
		return fmt.Errorf("node unset requires at least one attribute flag")
	}
	n, err := nodes.Update(ctx, client, id, ops).Extract()
	if err != nil {
		return fmt.Errorf("updating baremetal node %s: %w", id, err)
	}
	fields, values := nodeShowFields(n)
	return o.WriteSingle(w, fields, values)
}
