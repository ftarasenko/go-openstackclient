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
	"os"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/keypairs"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/paging"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
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
		newServerEventCommand(a, o),
		newServerEvacuateCommand(a, o),
		newServerAddCommand(a, o),
		newServerRemoveCommand(a, o),
		newServerConsoleCommand(a, o),
		newServerShelveCommand(a, o),
		newServerUnshelveCommand(a, o),
		newServerRescueCommand(a, o),
		newServerUnrescueCommand(a, o),
		newServerImageCommand(a, o),
		newServerGroupCommand(a, o),
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
	cmd.AddCommand(newServerAddVolumeCommand(a, o), floating, security, newServerAddServerGroupCommand(a, o),
		newServerAddPortCommand(a, o), newServerAddNetworkCommand(a, o), newServerAddFixedIPCommand(a, o))
	return cmd
}

// newServerRemoveCommand groups the "server remove ..." detachments.
func newServerRemoveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "remove", Short: "Detach a resource from a server"}
	floating := &cobra.Command{Use: "floating", Short: "Floating IP detachment"}
	floating.AddCommand(newServerRemoveFloatingIPCommand(a, o))
	security := &cobra.Command{Use: "security", Short: "Security group detachment"}
	security.AddCommand(newServerRemoveSecurityGroupCommand(a, o))
	cmd.AddCommand(newServerRemoveVolumeCommand(a, o), floating, security, newServerRemoveServerGroupCommand(a, o),
		newServerRemovePortCommand(a, o), newServerRemoveNetworkCommand(a, o), newServerRemoveFixedIPCommand(a, o))
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

	project       string
	projectDomain string
	user          string
	userDomain    string

	// pinMicroversion is set when the operator left the compute microversion at
	// koc's default, letting the list call negotiate the lowest version that
	// still answers it — see serverListMicroversion.
	pinMicroversion bool
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
			client, session, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, userID, err := resolveServerOwner(ctx, session, f)
			if err != nil {
				return err
			}
			f.pinMicroversion = a.ComputeAPIVersionPinnable()
			return runServerList(ctx, client, o, f, projectID, userID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.all, "all", false, "list servers across all projects (admin); alias of --all-projects")
	fl.BoolVar(&f.allProjects, "all-projects", false, "list servers across all projects (admin)")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.StringVar(&f.name, "name", "", "filter by server name (regular expression)")
	fl.StringVar(&f.status, "status", "", "filter by server status, e.g. ACTIVE")
	fl.StringVar(&f.host, "host", "", "filter by hypervisor host name")
	fl.StringVar(&f.project, "project", "", "list servers owned by this project (name or ID); implies --all-projects")
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project, to disambiguate the name (name or ID)")
	fl.StringVar(&f.user, "user", "", "list servers created by this user (name or ID); implies --all-projects")
	fl.StringVar(&f.userDomain, "user-domain", "", "domain owning --user, to disambiguate the name (name or ID)")
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

// resolveServerOwner turns --project / --user into keystone IDs, deriving the
// identity client only when a reference is a name rather than a UUID.
func resolveServerOwner(ctx context.Context, session *auth.Client, f *serverListFlags) (projectID, userID string, err error) {
	needsLookup := func(ref string) bool { return ref != "" && !resolve.IsUUID(ref) }
	if !needsLookup(f.project) && !needsLookup(f.user) {
		return f.project, f.user, nil
	}
	identity, err := session.Identity()
	if err != nil {
		return "", "", err
	}
	projectID, userID = f.project, f.user
	if needsLookup(f.project) {
		projectID, err = resolve.ProjectIDInDomain(ctx, identity, f.project, f.projectDomain)
		if err != nil {
			return "", "", err
		}
	}
	if needsLookup(f.user) {
		userID, err = resolve.UserIDInDomain(ctx, identity, f.user, f.userDomain)
		if err != nil {
			return "", "", err
		}
	}
	return projectID, userID, nil
}

func runServerList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *serverListFlags, projectID, userID string, w io.Writer,
) error {
	opts := serverListQuery{
		ListOpts: servers.ListOpts{
			Name:   f.name,
			Status: f.status,
			Host:   f.host,
			// Listing another project's or user's servers is a cross-project read,
			// which nova only honors together with all_tenants — so either filter
			// implies it rather than quietly returning nothing.
			AllTenants: f.all || f.allProjects || projectID != "" || userID != "",
			TenantID:   projectID,
			UserID:     userID,
			Marker:     f.marker,
			Limit:      f.limit,
		},
		Deleted:       f.deleted,
		CreatedSince:  f.createdSince,
		CreatedBefore: f.createdBefore,
		DeletedSince:  f.deletedSince,
		DeletedBefore: f.deletedBefore,
	}
	listClient := client
	if f.pinMicroversion {
		// setMicroversionHeader rewrites the header from client.Microversion on
		// every request, so the version is lowered on a shallow copy of the
		// service client rather than through RequestOpts (same pattern as
		// serverActionRaw).
		pinned := *client
		pinned.Microversion = serverListMicroversion(f)
		listClient = &pinned
	}
	// Nova treats limit only as a page size, so --limit is enforced as a hard
	// result cap; Collect also stops paging once it is met.
	pager := servers.List(listClient, opts)
	basic := serverListBasic(o, f)
	if basic {
		pager = servers.ListSimple(listClient, opts)
	}
	all, err := paging.Collect(ctx, pager, f.limit, servers.ExtractServers)
	if err != nil {
		if f.createdSince != "" || f.createdBefore != "" || f.deletedSince != "" || f.deletedBefore != "" {
			return keystackExtErr(fmt.Errorf("listing servers: %w", err), "created/deleted server-list filters")
		}
		return fmt.Errorf("listing servers: %w", err)
	}
	if basic {
		return o.WriteList(w, serverBasicTable(all))
	}
	var flavorNames map[string]string
	if f.long {
		flavorNames = serverFlavorNames(ctx, client, all)
	}
	return o.WriteList(w, serverListTable(all, f.long, flavorNames))
}

// serverListMicroversion is the lowest compute microversion that still answers
// the request in full.
//
// koc negotiates "latest", but nova has no way to select fields and widens
// every entry of /servers/detail as the microversion climbs: 2.3 alone adds
// OS-EXT-SRV-ATTR:user_data, which grew one measured listing twentyfold — none
// of it displayed. Each version below is the one nova's own query schema
// requires, so the rendered table is unchanged.
func serverListMicroversion(f *serverListFlags) string {
	// Ordered lowest requirement first; the last match wins.
	mv := "2.1"
	if f.createdSince != "" || f.createdBefore != "" || f.deletedSince != "" || f.deletedBefore != "" {
		// The KeyStack created-/deleted-* filters only enter nova's query schema
		// at 2.66 (api/openstack/compute/schemas/servers.py query_params_v266).
		mv = "2.66"
	}
	if f.user != "" {
		// user_id is rejected for a non-admin token below 2.83.
		mv = "2.83"
	}
	return mv
}

// serverListBasic reports whether nova's non-detail listing (GET /servers,
// which returns id/name/links only) can answer the request. It costs a fraction
// of /servers/detail, so the scripting idiom "server list -c ID -f value" takes
// it.
func serverListBasic(o *output.Options, f *serverListFlags) bool {
	return !f.long && o.ColumnsWithin("ID", "Name")
}

func serverBasicTable(list []servers.Server) output.Table {
	t := output.Table{Columns: []string{"ID", "Name"}, Rows: make([][]any, 0, len(list))}
	for _, s := range list {
		t.Rows = append(t.Rows, []any{s.ID, s.Name})
	}
	return t
}

func serverListTable(list []servers.Server, long bool, flavorNames map[string]string) output.Table {
	cols := []string{"ID", "Name", "Status", "Networks"}
	if long {
		cols = append(cols, "Image", "Flavor", "Availability Zone", "Host", "Task State", "Power State")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for _, s := range list {
		row := []any{s.ID, s.Name, s.Status, formatNetworks(s.Addresses)}
		if long {
			row = append(row, imageID(s.Image), flavorName(s.Flavor, flavorNames), s.AvailabilityZone, s.Host, s.TaskState, s.PowerState)
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
	// json/yaml keep the raw structured values so they can be parsed; the
	// text views (table/csv/value) flatten them OSC-style.
	flatten := o.Format != output.FormatJSON && o.Format != output.FormatYAML
	fields, values := showServerFields(body.Server, flatten)
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
		imageID(detail.Image), flavorName(detail.Flavor, nil), adminPass,
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
	name        string
	description string
	hostname    string
	properties  []string
	state       string
	tags        []string
	password    string
	noPassword  bool
	// availabilityZone drives the KeyStack per-instance AZ update (KCP-1211):
	// a server PUT carrying availability_zone (nova 2.90+). Vanilla nova's
	// server-update schema rejects the field with HTTP 400.
	availabilityZone string
}

func newServerSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &serverSetFlags{}
	var rootPassword bool
	cmd := &cobra.Command{
		Use:   "set <server>",
		Short: "Set server properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			// --root-password prompts instead of taking a value, so the password
			// is resolved here and the seam only ever sees the final string.
			if rootPassword {
				pw, err := promptNewPassword(cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				f.password = pw
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
	fl.StringVar(&f.description, "description", "", "new description for the server (nova 2.19+)")
	fl.StringVar(&f.hostname, "hostname", "", "hostname published to the metadata service (nova 2.90+)")
	fl.StringArrayVar(&f.properties, "property", nil, "metadata to set as key=value; repeatable")
	fl.StringVar(&f.state, "state", "", "reset the server state to active or error (admin only)")
	fl.StringArrayVar(&f.tags, "tag", nil, "tag to add to the server; repeatable (nova 2.26+)")
	fl.StringVar(&f.password, "password", "", "set the server's admin password (requires cloud support)")
	fl.BoolVar(&f.noPassword, "no-password", false,
		"clear the admin password from the metadata service; does not change the actual server password")
	// Deprecated upstream alias that prompts for the password instead of taking
	// it as a value; hidden there too, kept so existing scripts keep working.
	fl.BoolVar(&rootPassword, "root-password", false, "prompt for the server's new admin password")
	_ = fl.MarkHidden("root-password")
	cmd.MarkFlagsMutuallyExclusive("password", "no-password", "root-password")
	// KeyStack per-instance AZ change (KCP-1211), nova 2.90+; rejected by vanilla
	// nova. The fork spells the flag with an underscore, kept as an alias.
	fl.StringVar(&f.availabilityZone, "availability-zone", "", "KeyStack: move the server to a new availability zone")
	fl.StringVar(&f.availabilityZone, "availability_zone", "", "alias of --availability-zone")
	return cmd
}

// serverUpdateOpts extends gophercloud's servers.UpdateOpts, which carries no
// description field. The pointers distinguish "not set" from an empty string,
// which nova reads as "clear this field".
type serverUpdateOpts struct {
	Name        string  `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Hostname    *string `json:"hostname,omitempty"`
}

func (opts serverUpdateOpts) ToServerUpdateMap() (map[string]any, error) {
	return gophercloud.BuildRequestBody(opts, "server")
}

func runServerSet(ctx context.Context, client *gophercloud.ServiceClient, ref string, f *serverSetFlags, _ io.Writer) error {
	if err := validateServerSetFlags(client, f); err != nil {
		return err
	}
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	// One PUT for every standard updatable attribute, as OSC does; the KeyStack
	// availability_zone extension stays a separate call so its error can be
	// annotated without implicating a plain rename.
	opts := serverUpdateOpts{Name: f.name}
	if f.description != "" {
		opts.Description = &f.description
	}
	if f.hostname != "" {
		opts.Hostname = &f.hostname
	}
	if opts != (serverUpdateOpts{}) {
		if _, err := servers.Update(ctx, client, id, opts).Extract(); err != nil {
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
	if f.state != "" {
		if err := servers.ResetState(ctx, client, id, servers.ServerState(f.state)).ExtractErr(); err != nil {
			return fmt.Errorf("resetting state of server %q to %q: %w", ref, f.state, err)
		}
	}
	switch {
	case f.password != "":
		if err := servers.ChangeAdminPassword(ctx, client, id, f.password).ExtractErr(); err != nil {
			return fmt.Errorf("changing admin password on server %q: %w", ref, err)
		}
	case f.noPassword:
		if err := clearServerPassword(ctx, client, id); err != nil {
			return fmt.Errorf("clearing admin password on server %q: %w", ref, err)
		}
	}
	for _, tag := range f.tags {
		if err := addServerTag(ctx, client, id, tag); err != nil {
			return fmt.Errorf("adding tag %q to server %q: %w", tag, ref, err)
		}
	}
	return nil
}

// validateServerSetFlags rejects flag values the target API cannot honor before
// any request is issued, so a partially-applied "server set" is less likely.
func validateServerSetFlags(client *gophercloud.ServiceClient, f *serverSetFlags) error {
	if f.state != "" && f.state != string(servers.StateActive) && f.state != string(servers.StateError) {
		return fmt.Errorf("invalid --state %q: want active or error", f.state)
	}
	for _, tag := range f.tags {
		// Nova forbids "/" in a tag, and it would also split the tag URL path.
		if tag == "" || strings.Contains(tag, "/") {
			return fmt.Errorf("invalid --tag %q: must be non-empty and contain no %q", tag, "/")
		}
	}
	for _, need := range []struct {
		set  bool
		flag string
		mv   string
	}{
		{f.description != "", "--description", "2.19"},
		{len(f.tags) > 0, "--tag", "2.26"},
		{f.hostname != "", "--hostname", "2.90"},
	} {
		if need.set && !computeSupportsMicroversion(client, need.mv) {
			return fmt.Errorf("%s requires compute API microversion %s or later (--os-compute-api-version)", need.flag, need.mv)
		}
	}
	return nil
}

// addServerTag adds a single tag to a server (nova 2.26+). gophercloud v2 has no
// typed server-tags API, so this is a raw PUT; nova answers 201 when the tag is
// new and 204 when it was already present.
func addServerTag(ctx context.Context, client *gophercloud.ServiceClient, id, tag string) error {
	url := client.ServiceURL("servers", id, "tags", tag)
	resp, err := client.Put(ctx, url, nil, nil, &gophercloud.RequestOpts{OkCodes: []int{201, 204}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// clearServerPassword drops the admin password nova published to the metadata
// service. This does not change the password inside the guest. gophercloud v2
// exposes the GET but not the DELETE, so issue it raw.
func clearServerPassword(ctx context.Context, client *gophercloud.ServiceClient, id string) error {
	url := client.ServiceURL("servers", id, "os-server-password")
	resp, err := client.Delete(ctx, url, &gophercloud.RequestOpts{OkCodes: []int{202, 204}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// promptNewPassword reads a new admin password twice without echo, mirroring
// "openstack server set --root-password".
func promptNewPassword(w io.Writer) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("--root-password needs an interactive terminal; use --password instead")
	}
	read := func(prompt string) (string, error) {
		if _, err := fmt.Fprint(w, prompt); err != nil {
			return "", err
		}
		pw, err := term.ReadPassword(fd)
		if _, perr := fmt.Fprintln(w); perr != nil && err == nil {
			err = perr
		}
		if err != nil {
			return "", fmt.Errorf("reading password: %w", err)
		}
		return string(pw), nil
	}
	first, err := read("New password: ")
	if err != nil {
		return "", err
	}
	second, err := read("Retype new password: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("passwords do not match, password unchanged")
	}
	return first, nil
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
