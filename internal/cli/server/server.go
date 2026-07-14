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
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"

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
		newServerMigrationCommand(a, o),
		newServerEvacuateCommand(a, o),
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
	cmd.AddCommand(newServerAddVolumeCommand(a, o), floating, security, newServerAddServerGroupCommand(a, o))
	return cmd
}

// newServerRemoveCommand groups the "server remove ..." detachments.
func newServerRemoveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "remove", Short: "Detach a resource from a server"}
	floating := &cobra.Command{Use: "floating", Short: "Floating IP detachment"}
	floating.AddCommand(newServerRemoveFloatingIPCommand(a, o))
	security := &cobra.Command{Use: "security", Short: "Security group detachment"}
	security.AddCommand(newServerRemoveSecurityGroupCommand(a, o))
	cmd.AddCommand(newServerRemoveVolumeCommand(a, o), floating, security, newServerRemoveServerGroupCommand(a, o))
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
		newAggregateCommand(a, o),
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
	// KeyStack server-list extensions (KCP-1768 time filters, KCP-2417 deleted):
	// created-/deleted-* are extra query params nova 2.66+ does not implement
	// upstream; --deleted restricts the list to deleted servers. All are sent
	// only when set, so the default query is byte-identical to vanilla nova.
	deleted       bool
	createdSince  string
	createdBefore string
	deletedSince  string
	deletedBefore string
}

// serverListQuery augments gophercloud's servers.ListOpts with the KeyStack
// server-list filters, which have no typed fields. It satisfies ListOptsBuilder
// by appending the extra params to the base query string.
type serverListQuery struct {
	servers.ListOpts
	Deleted       bool
	CreatedSince  string
	CreatedBefore string
	DeletedSince  string
	DeletedBefore string
}

func (q serverListQuery) ToServerListQuery() (string, error) {
	base, err := q.ListOpts.ToServerListQuery()
	if err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	vals := u.Query()
	if q.Deleted {
		vals.Set("deleted", "true")
	}
	for key, val := range map[string]string{
		"created-since":  q.CreatedSince,
		"created-before": q.CreatedBefore,
		"deleted-since":  q.DeletedSince,
		"deleted-before": q.DeletedBefore,
	} {
		if val != "" {
			vals.Set(key, val)
		}
	}
	u.RawQuery = vals.Encode()
	return u.String(), nil
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
	// KeyStack server-list filters (KCP-1768/2417), nova 2.66+; rejected by
	// vanilla nova. Times are ISO 8601, e.g. 2016-03-04T06:27:59Z.
	fl.BoolVar(&f.deleted, "deleted", false, "only list deleted servers (admin)")
	fl.StringVar(&f.createdSince, "created-since", "", "KeyStack: only servers created at/after this ISO-8601 time")
	fl.StringVar(&f.createdBefore, "created-before", "", "KeyStack: only servers created at/before this ISO-8601 time")
	fl.StringVar(&f.deletedSince, "deleted-since", "", "KeyStack: only servers deleted at/after this ISO-8601 time (use with --deleted)")
	fl.StringVar(&f.deletedBefore, "deleted-before", "", "KeyStack: only servers deleted at/before this ISO-8601 time (use with --deleted)")
	return cmd
}

func runServerList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *serverListFlags, w io.Writer) error {
	opts := serverListQuery{
		ListOpts: servers.ListOpts{
			Name:       f.name,
			Status:     f.status,
			Host:       f.host,
			AllTenants: f.all || f.allProjects,
			Marker:     f.marker,
			Limit:      f.limit,
		},
		Deleted:       f.deleted,
		CreatedSince:  f.createdSince,
		CreatedBefore: f.createdBefore,
		DeletedSince:  f.deletedSince,
		DeletedBefore: f.deletedBefore,
	}
	pages, err := servers.List(client, opts).AllPages(ctx)
	if err != nil {
		if f.createdSince != "" || f.createdBefore != "" || f.deletedSince != "" || f.deletedBefore != "" {
			return keystackExtErr(fmt.Errorf("listing servers: %w", err), "created/deleted server-list filters")
		}
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
	var userData bool
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
			return runServerShow(ctx, client, o, args[0], userData, cmd.OutOrStdout())
		},
	}
	// user_data is a large base64 blob elided from the default table; --user-data
	// dumps just the decoded cloud-init/script so it can be piped or read.
	cmd.Flags().BoolVar(&userData, "user-data", false,
		"output only the server's user_data, base64-decoded")
	return cmd
}

func runServerShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, userData bool, w io.Writer) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	// Show every attribute nova returns, matching the breadth of `openstack
	// server show`. The typed servers.Server struct exposes only a curated
	// subset (and drops the OS-EXT-* admin attributes), so decode the raw
	// object instead. Narrow the view with -c/--column or -f json/yaml.
	var body struct {
		Server map[string]any `json:"server"`
	}
	resp, err := client.Get(ctx, client.ServiceURL("servers", id), &body, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return fmt.Errorf("showing server %q: %w", ref, err)
	}
	if userData {
		return writeServerUserData(body.Server, w)
	}
	fields, values := showAllServerFields(body.Server)
	return o.WriteSingle(w, fields, values)
}

// writeServerUserData decodes the server's base64 user_data and writes it raw.
// It errors when the server carries no user_data; a value that is not valid
// base64 is written through unchanged (nova stores it verbatim).
func writeServerUserData(server map[string]any, w io.Writer) error {
	raw, _ := server["OS-EXT-SRV-ATTR:user_data"].(string)
	if raw == "" {
		return fmt.Errorf("server has no user_data")
	}
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
		_, err := w.Write(decoded)
		return err
	}
	_, err := io.WriteString(w, raw)
	return err
}

// serverCreateFlags holds the parameters accepted by "server create".
type serverCreateFlags struct {
	image          string
	flavor         string
	networks       []string
	nics           []string
	nicSpecs       []nicSpec
	keyName        string
	configDrive    bool
	configDriveSet bool
	securityGroups []string
	properties     []string
	bootFromVolume int
	bootVolumeType string
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
			// preserving order, then parse each into a nicSpec (accepting both a
			// bare network ref and the OSC "net-id=<id>,..." key=value form).
			for _, raw := range append(append([]string{}, f.networks...), f.nics...) {
				spec, err := parseNIC(raw)
				if err != nil {
					return err
				}
				f.nicSpecs = append(f.nicSpecs, spec)
			}
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
	// --network and --nic are accepted as aliases: each value is a network ID or
	// name, or the upstream OSC form "net-id=<id>,v4-fixed-ip=<ip>" /
	// "port-id=<id>" (net-name is resolved to an ID like a bare name).
	fl.StringArrayVar(&f.networks, "network", nil, "network to attach: an ID/name, or net-id=/net-name=/port-id=/v4-fixed-ip= pairs; repeatable")
	fl.StringArrayVar(&f.nics, "nic", nil, "alias of --network")
	fl.StringVar(&f.keyName, "key-name", "", "name of the keypair to inject")
	// Boolean: "--config-drive" (bare) enables it; "--config-drive=true|false"
	// sets it explicitly. The space form "--config-drive true" is not supported —
	// pflag cannot both default the bare flag and consume a separate value.
	fl.BoolVar(&f.configDrive, "config-drive", false, "enable a config drive (bare, or --config-drive=true|false)")
	fl.StringArrayVar(&f.securityGroups, "security-group", nil, "security group name; repeatable")
	fl.StringArrayVar(&f.properties, "property", nil, "server metadata as key=value; repeatable")
	// --boot-from-volume <size-GB> boots the server from a new volume of the
	// given size created from --image (block_device_mapping_v2, boot_index 0).
	// --boot-volume-type sets that volume's cinder type (needs compute API 2.67+;
	// the default microversion is "latest", so it is available by default).
	fl.IntVar(&f.bootFromVolume, "boot-from-volume", 0, "boot from a new volume of this size in GB, created from --image")
	fl.StringVar(&f.bootVolumeType, "boot-volume-type", "", "cinder volume type for the --boot-from-volume root volume")
	fl.IntVar(&f.min, "min", 0, "minimum number of servers to launch")
	fl.IntVar(&f.max, "max", 0, "maximum number of servers to launch")
	return cmd
}

func runServerCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *serverCreateFlags, w io.Writer) error {
	if f.flavor == "" {
		return fmt.Errorf("--flavor is required")
	}
	if f.bootFromVolume < 0 {
		return fmt.Errorf("--boot-from-volume size must not be negative")
	}
	if f.bootFromVolume > 0 && f.image == "" {
		return fmt.Errorf("--boot-from-volume requires --image (the volume is created from that image)")
	}
	if f.bootVolumeType != "" && f.bootFromVolume == 0 {
		return fmt.Errorf("--boot-volume-type requires --boot-from-volume")
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
	if len(f.nicSpecs) > 0 {
		nets := make([]servers.Network, 0, len(f.nicSpecs))
		for _, n := range f.nicSpecs {
			nets = append(nets, servers.Network{UUID: n.netRef, Port: n.port, FixedIP: n.fixedIP})
		}
		opts.Networks = nets
	}
	if f.bootFromVolume > 0 {
		// Boot from a new volume created from the image: move the image into a
		// block_device_mapping_v2 entry (boot_index 0, image → volume) and clear
		// the top-level imageRef, which nova rejects as a conflicting root device
		// when a boot-index-0 block device is also present (matches OSC).
		//
		// The created volume is left unnamed: nova's block_device_mapping_v2 has
		// no field for the resulting volume's display name, so naming it would
		// require pre-creating the volume via cinder and booting from it by ID.
		// That is out of scope here; the volume takes cinder's default name.
		opts.BlockDevice = []servers.BlockDevice{{
			BootIndex:       0,
			SourceType:      servers.SourceImage,
			UUID:            f.image,
			DestinationType: servers.DestinationVolume,
			VolumeSize:      f.bootFromVolume,
			VolumeType:      f.bootVolumeType,
		}}
		opts.ImageRef = ""
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
	// Nova's create response carries only the ID and the generated admin
	// password — name, status and networks are absent, so the raw response
	// renders a table with blank Name/Status. Re-fetch the server to show a
	// meaningful summary, preserving the admin password (Get never returns it).
	// A failed follow-up Get is non-fatal: the create already succeeded, so fall
	// back to the fields we hold.
	adminPass := s.AdminPass
	detail, gerr := servers.Get(ctx, client, s.ID).Extract()
	if gerr != nil {
		return o.WriteSingle(w, []string{"ID", "Name", "Admin Password"}, []any{s.ID, name, adminPass})
	}
	fields := []string{"ID", "Name", "Status", "Networks", "Image", "Flavor", "Admin Password"}
	values := []any{
		detail.ID, detail.Name, detail.Status, formatNetworks(detail.Addresses),
		imageID(detail.Image), flavorName(detail.Flavor), adminPass,
	}
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
	// availabilityZone drives the KeyStack per-instance AZ update (KCP-1211):
	// a server PUT carrying availability_zone (nova 2.90+). Vanilla nova's
	// server-update schema rejects the field with HTTP 400.
	availabilityZone string
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
	// KeyStack per-instance AZ change (KCP-1211), nova 2.90+; rejected by vanilla
	// nova. The fork spells the flag with an underscore, kept as an alias.
	fl.StringVar(&f.availabilityZone, "availability-zone", "", "KeyStack: move the server to a new availability zone")
	fl.StringVar(&f.availabilityZone, "availability_zone", "", "alias of --availability-zone")
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
	if f.availabilityZone != "" {
		// gophercloud's servers.UpdateOpts has no availability_zone field, so
		// issue the raw PUT /servers/{id} the KeyStack extension expects.
		body := map[string]any{"server": map[string]any{"availability_zone": f.availabilityZone}}
		resp, err := client.Put(ctx, client.ServiceURL("servers", id), body, nil, &gophercloud.RequestOpts{OkCodes: []int{200}})
		if resp != nil {
			defer func() { _ = resp.Body.Close() }()
		}
		if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
			return keystackExtErr(fmt.Errorf("updating availability zone on server %q: %w", ref, err), "per-instance availability_zone update")
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
