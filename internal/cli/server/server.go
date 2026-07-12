// Package server implements the "koc server", "koc compute" and
// "koc hypervisor" command trees plus "koc quota show", mirroring the upstream
// "openstack server / compute service / hypervisor / quota" (nova) surface.
//
// Flag names follow upstream python-openstackclient (OSC). The KeyStack command
// reference at https://docs.keystack.ru/ was not reachable at implementation
// time (HTTP 403), so flag semantics are UNVERIFIED against KeyStack and fall
// back to upstream OSC — see the PR description.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/keypairs"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand builds just the "server" command group. It exists so the root
// command can keep a single, familiar entrypoint; the sibling "compute",
// "hypervisor" and "quota" groups are returned by NewCommands.
func NewCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Compute server (instance) commands",
	}
	cmd.AddCommand(
		newServerListCommand(a, o),
		newServerShowCommand(a, o),
		newServerCreateCommand(a, o),
		newServerDeleteCommand(a, o),
		newServerSetCommand(a, o),
		newServerUnsetCommand(a, o),
		newServerStartCommand(a, o),
		newServerStopCommand(a, o),
		newServerRebootCommand(a, o),
		newServerPauseCommand(a, o),
		newServerUnpauseCommand(a, o),
		newServerSuspendCommand(a, o),
		newServerResumeCommand(a, o),
		newServerLockCommand(a, o),
		newServerUnlockCommand(a, o),
		newServerResizeCommand(a, o),
		newServerRebuildCommand(a, o),
		newServerMigrateCommand(a, o),
		newServerAddCommand(a, o),
		newServerRemoveCommand(a, o),
		newServerConsoleCommand(a, o),
	)
	return cmd
}

// newServerAddCommand groups the "server add ..." resource attachments, mirroring
// OSC (`server add volume`, `server add floating ip`, `server add security
// group`). Each leaf lives under an "add" parent so the two-word nouns resolve
// unambiguously.
func newServerAddCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "add", Short: "Attach a resource to a server"}
	floating := &cobra.Command{Use: "floating", Short: "Floating IP attachment"}
	floating.AddCommand(newServerAddFloatingIPCommand(a, o))
	security := &cobra.Command{Use: "security", Short: "Security group attachment"}
	security.AddCommand(newServerAddSecurityGroupCommand(a, o))
	cmd.AddCommand(newServerAddVolumeCommand(a, o), floating, security)
	return cmd
}

// newServerRemoveCommand groups the "server remove ..." detachments.
func newServerRemoveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "remove", Short: "Detach a resource from a server"}
	floating := &cobra.Command{Use: "floating", Short: "Floating IP detachment"}
	floating.AddCommand(newServerRemoveFloatingIPCommand(a, o))
	security := &cobra.Command{Use: "security", Short: "Security group detachment"}
	security.AddCommand(newServerRemoveSecurityGroupCommand(a, o))
	cmd.AddCommand(newServerRemoveVolumeCommand(a, o), floating, security)
	return cmd
}

// NewCommands returns every top-level command implemented by this package:
// "server", "compute" (parent of "compute service ..."), "hypervisor" and
// "quota". A single builder cannot return multiple siblings, so the caller wires
// each of these onto the root command.
func NewCommands(a *auth.Options, o *output.Options) []*cobra.Command {
	return []*cobra.Command{
		NewCommand(a, o),
		newComputeCommand(a, o),
		newHypervisorCommand(a, o),
		newQuotaCommand(a, o),
	}
}

// serverListFlags holds the filters accepted by "server list".
type serverListFlags struct {
	all         bool
	allProjects bool
	long        bool
	name        string
	status      string
	host        string
	limit       int
	marker      string
}

func newServerListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &serverListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List compute servers",
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
			return runServerList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.all, "all", false, "list servers across all projects (admin); alias of --all-projects")
	fl.BoolVar(&f.allProjects, "all-projects", false, "list servers across all projects (admin)")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.StringVar(&f.name, "name", "", "filter by server name (regular expression)")
	fl.StringVar(&f.status, "status", "", "filter by server status, e.g. ACTIVE")
	fl.StringVar(&f.host, "host", "", "filter by hypervisor host name")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of servers to return")
	fl.StringVar(&f.marker, "marker", "", "list servers after this server ID (pagination marker)")
	return cmd
}

func runServerList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *serverListFlags, w io.Writer) error {
	opts := servers.ListOpts{
		Name:       f.name,
		Status:     f.status,
		Host:       f.host,
		AllTenants: f.all || f.allProjects,
		Marker:     f.marker,
		Limit:      f.limit,
	}
	pages, err := servers.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing servers: %w", err)
	}
	all, err := servers.ExtractServers(pages)
	if err != nil {
		return fmt.Errorf("parsing server list: %w", err)
	}
	// Nova treats limit only as a page size, so AllPages may return more than
	// requested; enforce --limit as a hard result cap.
	if f.limit > 0 && len(all) > f.limit {
		all = all[:f.limit]
	}
	return o.WriteList(w, serverListTable(all, f.long))
}

func serverListTable(list []servers.Server, long bool) output.Table {
	cols := []string{"ID", "Name", "Status", "Networks"}
	if long {
		cols = append(cols, "Image", "Flavor", "Availability Zone", "Host", "Task State", "Power State")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for _, s := range list {
		row := []any{s.ID, s.Name, s.Status, formatNetworks(s.Addresses)}
		if long {
			row = append(row, imageID(s.Image), flavorName(s.Flavor), s.AvailabilityZone, s.Host, s.TaskState, s.PowerState)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

func newServerShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <server>",
		Short: "Show details of a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}

func runServerShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	s, err := servers.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing server %q: %w", ref, err)
	}
	fields := []string{
		"ID", "Name", "Status", "Networks", "Image", "Flavor", "Key Name",
		"Availability Zone", "Host", "Task State", "Power State",
		"Created", "Updated", "Project ID", "User ID", "Metadata", "Security Groups",
	}
	secGroups := make([]string, 0, len(s.SecurityGroups))
	for _, g := range s.SecurityGroups {
		if n, ok := g["name"].(string); ok {
			secGroups = append(secGroups, n)
		}
	}
	values := []any{
		s.ID, s.Name, s.Status, formatNetworks(s.Addresses), imageID(s.Image), flavorName(s.Flavor), s.KeyName,
		s.AvailabilityZone, s.Host, s.TaskState, s.PowerState,
		s.Created.String(), s.Updated.String(), s.TenantID, s.UserID, s.Metadata, strings.Join(secGroups, ", "),
	}
	return o.WriteSingle(w, fields, values)
}

// serverCreateFlags holds the parameters accepted by "server create".
type serverCreateFlags struct {
	image          string
	flavor         string
	networks       []string
	nics           []string
	keyName        string
	configDrive    bool
	configDriveSet bool
	securityGroups []string
	properties     []string
	min            int
	max            int
}

func newServerCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &serverCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.configDriveSet = cmd.Flags().Changed("config-drive")
			// --network and --nic are aliases bound to separate slices so
			// mixing them does not clobber values. Merge --nic after --network,
			// preserving order, before resolution and use.
			f.networks = append(f.networks, f.nics...)
			ctx := cmd.Context()
			client, session, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			// Resolve cross-service references (image → glance, network →
			// neutron) to IDs before building the create request.
			if err := resolveServerCreateRefs(ctx, session, f); err != nil {
				return err
			}
			return runServerCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.image, "image", "", "image ID or name to boot from")
	fl.StringVar(&f.flavor, "flavor", "", "flavor ID or name (required)")
	// --network and --nic are accepted as aliases for the same value: a network ID/name to attach.
	fl.StringArrayVar(&f.networks, "network", nil, "network ID or name to attach; repeatable")
	fl.StringArrayVar(&f.nics, "nic", nil, "alias of --network")
	fl.StringVar(&f.keyName, "key-name", "", "name of the keypair to inject")
	fl.BoolVar(&f.configDrive, "config-drive", false, "enable a config drive")
	fl.StringArrayVar(&f.securityGroups, "security-group", nil, "security group name; repeatable")
	fl.StringArrayVar(&f.properties, "property", nil, "server metadata as key=value; repeatable")
	fl.IntVar(&f.min, "min", 0, "minimum number of servers to launch")
	fl.IntVar(&f.max, "max", 0, "maximum number of servers to launch")
	return cmd
}

func runServerCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *serverCreateFlags, w io.Writer) error {
	if f.flavor == "" {
		return fmt.Errorf("--flavor is required")
	}
	flavorRef, err := resolveFlavorRef(ctx, client, f.flavor)
	if err != nil {
		return err
	}
	metadata, err := parseKeyValStrings(f.properties)
	if err != nil {
		return err
	}

	opts := servers.CreateOpts{
		Name:           name,
		ImageRef:       f.image,
		FlavorRef:      flavorRef,
		SecurityGroups: f.securityGroups,
		Metadata:       metadata,
		Min:            f.min,
		Max:            f.max,
	}
	if f.configDriveSet {
		cd := f.configDrive
		opts.ConfigDrive = &cd
	}
	if len(f.networks) > 0 {
		nets := make([]servers.Network, 0, len(f.networks))
		for _, n := range f.networks {
			nets = append(nets, servers.Network{UUID: n})
		}
		opts.Networks = nets
	}

	// key_name is not a field of servers.CreateOpts; it is injected by wrapping
	// the base opts with keypairs.CreateOptsExt.
	var createOpts servers.CreateOptsBuilder = opts
	if f.keyName != "" {
		createOpts = keypairs.CreateOptsExt{CreateOptsBuilder: opts, KeyName: f.keyName}
	}

	s, err := servers.Create(ctx, client, createOpts, nil).Extract()
	if err != nil {
		return fmt.Errorf("creating server %q: %w", name, err)
	}
	fields := []string{"ID", "Name", "Status", "Admin Password"}
	values := []any{s.ID, s.Name, s.Status, s.AdminPass}
	return o.WriteSingle(w, fields, values)
}

func newServerDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <server> [<server> ...]",
		Short: "Delete one or more servers",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runServerDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	// Attempt every ref; collect failures so one bad server does not prevent the
	// rest from being deleted, then report all errors together.
	var errs []error
	for _, ref := range refs {
		id, err := resolveServerID(ctx, client, ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := servers.Delete(ctx, client, id).ExtractErr(); err != nil {
			errs = append(errs, fmt.Errorf("deleting server %q: %w", ref, err))
			continue
		}
		if _, err := fmt.Fprintf(w, "Deleted server %s\n", ref); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// serverSetFlags holds the mutable attributes accepted by "server set".
type serverSetFlags struct {
	name       string
	properties []string
}

func newServerSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &serverSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <server>",
		Short: "Set server properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerSet(ctx, client, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new name for the server")
	fl.StringArrayVar(&f.properties, "property", nil, "metadata to set as key=value; repeatable")
	return cmd
}

func runServerSet(ctx context.Context, client *gophercloud.ServiceClient, ref string, f *serverSetFlags, _ io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	if f.name != "" {
		if _, err := servers.Update(ctx, client, id, servers.UpdateOpts{Name: f.name}).Extract(); err != nil {
			return fmt.Errorf("updating server %q: %w", ref, err)
		}
	}
	if len(f.properties) > 0 {
		meta, err := parseKeyValStrings(f.properties)
		if err != nil {
			return err
		}
		if _, err := servers.UpdateMetadata(ctx, client, id, servers.MetadataOpts(meta)).Extract(); err != nil {
			return fmt.Errorf("updating metadata on server %q: %w", ref, err)
		}
	}
	return nil
}

func newServerUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var properties []string
	cmd := &cobra.Command{
		Use:   "unset <server>",
		Short: "Unset server properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerUnset(ctx, client, args[0], properties, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&properties, "property", nil, "metadata key to remove; repeatable")
	return cmd
}

func runServerUnset(ctx context.Context, client *gophercloud.ServiceClient, ref string, properties []string, _ io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	keys := append([]string(nil), properties...)
	sort.Strings(keys)
	for _, k := range keys {
		if err := servers.DeleteMetadatum(ctx, client, id, k).ExtractErr(); err != nil {
			return fmt.Errorf("removing metadata %q from server %q: %w", k, ref, err)
		}
	}
	return nil
}
