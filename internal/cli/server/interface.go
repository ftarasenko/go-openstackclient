package server

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/attachinterfaces"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "server add/remove port|network|fixed ip" — nova's os-interface subresource,
// which attaches and detaches neutron ports on a running server.
//
// Flag names follow upstream OSC (`openstack server add port` and friends).
// UNVERIFIED against KeyStack docs (https://docs.keystack.ru/ returned HTTP 403
// at implementation time); falls back to upstream OSC semantics.

// interfaceTagMicroversion is where nova added `tag` to the attachment body
// (nova/api/openstack/compute/schemas/attach_interfaces.py, create_v249). The
// schema sets additionalProperties=false, so sending it to an older cloud is a
// 400 rather than an ignored field.
const interfaceTagMicroversion = "2.49"

// attachOpts extends gophercloud's CreateOpts with `tag`, which the typed
// struct does not model.
type attachOpts struct {
	attachinterfaces.CreateOpts
	Tag string
}

func (o attachOpts) ToAttachInterfacesCreateMap() (map[string]any, error) {
	body, err := o.CreateOpts.ToAttachInterfacesCreateMap()
	if err != nil {
		return nil, err
	}
	if o.Tag == "" {
		return body, nil
	}
	inner, ok := body["interfaceAttachment"].(map[string]any)
	if !ok {
		inner = map[string]any{}
	}
	inner["tag"] = o.Tag
	body["interfaceAttachment"] = inner
	return body, nil
}

// --- add port ---------------------------------------------------------------

func newServerAddPortCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var tag string
	cmd := &cobra.Command{
		Use:   "port <server> <port>",
		Short: "Attach a port to a server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			return runServerAddPort(ctx, s, o, args[0], args[1], tag, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&tag, "tag", "", helpInterfaceTag)
	return cmd
}

func runServerAddPort(ctx context.Context, s *computeSession, o *output.Options,
	serverRef, portRef, tag string, w io.Writer,
) error {
	id, err := resolveServerID(ctx, s.client, serverRef)
	if err != nil {
		return err
	}
	portID, err := resolveNetworkResource(ctx, s.auth, portRef, resolve.PortID)
	if err != nil {
		return err
	}
	opts, err := newAttachOpts(s.client, attachinterfaces.CreateOpts{PortID: portID}, tag)
	if err != nil {
		return err
	}
	iface, err := attachinterfaces.Create(ctx, s.client, id, opts).Extract()
	if err != nil {
		return fmt.Errorf("attaching port %q to server %q: %w", portRef, serverRef, err)
	}
	return writeInterface(o, w, iface)
}

// --- add network ------------------------------------------------------------

func newServerAddNetworkCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var tag string
	cmd := &cobra.Command{
		Use:   "network <server> <network>",
		Short: "Attach a new port on a network to a server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			return runServerAddNetwork(ctx, s, o, args[0], args[1], &attachFlags{tag: tag}, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&tag, "tag", "", helpInterfaceTag)
	return cmd
}

// --- add fixed ip -----------------------------------------------------------

func newServerAddFixedIPCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &attachFlags{}
	ip := &cobra.Command{
		Use:   "ip <server> <network>",
		Short: "Allocate a fixed IP on a network and attach it to a server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			return runServerAddNetwork(ctx, s, o, args[0], args[1], f, cmd.OutOrStdout())
		},
	}
	fl := ip.Flags()
	fl.StringVar(&f.address, "fixed-ip-address", "", "specific fixed IP address to request")
	fl.StringVar(&f.tag, "tag", "", helpInterfaceTag)

	cmd := &cobra.Command{Use: "fixed", Short: "Attach a fixed IP to a server"}
	cmd.AddCommand(ip)
	return cmd
}

// runServerAddNetwork backs both "add network" and "add fixed ip": nova has one
// endpoint for them, and the only difference is whether a specific address is
// requested. Nova rejects a fixed IP without a network (400), which is why the
// address rides along with net_id rather than being its own call.
// attachFlags are the options common to "add network" and "add fixed ip".
type attachFlags struct {
	address string
	tag     string
}

func runServerAddNetwork(ctx context.Context, s *computeSession, o *output.Options,
	serverRef, networkRef string, f *attachFlags, w io.Writer,
) error {
	id, err := resolveServerID(ctx, s.client, serverRef)
	if err != nil {
		return err
	}
	networkID, err := resolveNetworkResource(ctx, s.auth, networkRef, resolve.NetworkID)
	if err != nil {
		return err
	}
	if err := runServerAddNetworkForID(ctx, s.client, o, id, networkID, f, w); err != nil {
		return fmt.Errorf("attaching network %q to server %q: %w", networkRef, serverRef, err)
	}
	return nil
}

// runServerAddNetworkForID is the seam: it takes resolved IDs, so tests drive
// it against a mock endpoint without an auth session.
func runServerAddNetworkForID(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	serverID, networkID string, f *attachFlags, w io.Writer,
) error {
	create := attachinterfaces.CreateOpts{NetworkID: networkID}
	if f.address != "" {
		// nova's schema caps fixed_ips at one item and requires ip_address.
		create.FixedIPs = []attachinterfaces.FixedIP{{IPAddress: f.address}}
	}
	opts, err := newAttachOpts(client, create, f.tag)
	if err != nil {
		return err
	}
	iface, err := attachinterfaces.Create(ctx, client, serverID, opts).Extract()
	if err != nil {
		return err
	}
	return writeInterface(o, w, iface)
}

// --- remove port / network / fixed ip ---------------------------------------

func newServerRemovePortCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "port <server> <port>",
		Short: "Detach a port from a server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			return runServerRemovePort(ctx, s.client, s.auth, args[0], args[1], cmd.OutOrStdout())
		},
	}
}

func runServerRemovePort(ctx context.Context, client *gophercloud.ServiceClient, ac *auth.Client,
	serverRef, portRef string, w io.Writer,
) error {
	id, err := resolveServerID(ctx, client, serverRef)
	if err != nil {
		return err
	}
	portID, err := resolveNetworkResource(ctx, ac, portRef, resolve.PortID)
	if err != nil {
		return err
	}
	if err := attachinterfaces.Delete(ctx, client, id, portID).ExtractErr(); err != nil {
		return fmt.Errorf("detaching port %q from server %q: %w", portRef, serverRef, err)
	}
	_, err = fmt.Fprintf(w, "Detached port %s from server %s\n", portRef, serverRef)
	return err
}

func newServerRemoveNetworkCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "network <server> <network>",
		Short: "Detach every port on a network from a server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			return runServerRemoveNetwork(ctx, s.client, s.auth, args[0], args[1], cmd.OutOrStdout())
		},
	}
}

// runServerRemoveNetwork detaches every interface the server has on the given
// network. Nova's delete endpoint is keyed by port, not network, so the match
// happens client-side over the interface listing — the same thing upstream
// does.
func runServerRemoveNetwork(ctx context.Context, client *gophercloud.ServiceClient, ac *auth.Client,
	serverRef, networkRef string, w io.Writer,
) error {
	id, err := resolveServerID(ctx, client, serverRef)
	if err != nil {
		return err
	}
	networkID, err := resolveNetworkResource(ctx, ac, networkRef, resolve.NetworkID)
	if err != nil {
		return err
	}
	return runServerRemoveNetworkForID(ctx, client, id, networkID, w)
}

// runServerRemoveNetworkForID is the seam: it takes resolved IDs, so tests drive
// it against a mock endpoint without an auth session.
func runServerRemoveNetworkForID(ctx context.Context, client *gophercloud.ServiceClient,
	serverID, networkID string, w io.Writer,
) error {
	ifaces, err := listServerInterfaces(ctx, client, serverID)
	if err != nil {
		return fmt.Errorf("listing interfaces of server %s: %w", serverID, err)
	}
	var detached int
	for _, iface := range ifaces {
		if iface.NetID != networkID {
			continue
		}
		if err := attachinterfaces.Delete(ctx, client, serverID, iface.PortID).ExtractErr(); err != nil {
			return fmt.Errorf("detaching port %s from server %s: %w", iface.PortID, serverID, err)
		}
		detached++
	}
	if detached == 0 {
		return fmt.Errorf("server %s has no interface on network %s", serverID, networkID)
	}
	_, err = fmt.Fprintf(w, "Detached %d port(s) on network %s from server %s\n", detached, networkID, serverID)
	return err
}

func newServerRemoveFixedIPCommand(a *auth.Options, o *output.Options) *cobra.Command {
	ip := &cobra.Command{
		Use:   "ip <server> <ip-address>",
		Short: "Detach the port holding a fixed IP from a server",
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
			return runServerRemoveFixedIP(ctx, client, args[0], args[1], cmd.OutOrStdout())
		},
	}
	cmd := &cobra.Command{Use: "fixed", Short: "Detach a fixed IP from a server"}
	cmd.AddCommand(ip)
	return cmd
}

// runServerRemoveFixedIP finds the port carrying the address and detaches it.
// Nova has no delete-by-address endpoint, so the address is matched against the
// interface listing first.
func runServerRemoveFixedIP(ctx context.Context, client *gophercloud.ServiceClient,
	serverRef, address string, w io.Writer,
) error {
	id, err := resolveServerID(ctx, client, serverRef)
	if err != nil {
		return err
	}
	ifaces, err := listServerInterfaces(ctx, client, id)
	if err != nil {
		return fmt.Errorf("listing interfaces of server %q: %w", serverRef, err)
	}
	for _, iface := range ifaces {
		for _, fixed := range iface.FixedIPs {
			if fixed.IPAddress != address {
				continue
			}
			if err := attachinterfaces.Delete(ctx, client, id, iface.PortID).ExtractErr(); err != nil {
				return fmt.Errorf("detaching port %s from server %q: %w", iface.PortID, serverRef, err)
			}
			_, err := fmt.Fprintf(w, "Detached fixed IP %s from server %s\n", address, serverRef)
			return err
		}
	}
	return fmt.Errorf("server %q has no interface with fixed IP %s", serverRef, address)
}

// --- shared -----------------------------------------------------------------

func listServerInterfaces(ctx context.Context, client *gophercloud.ServiceClient, serverID string) ([]attachinterfaces.Interface, error) {
	pages, err := attachinterfaces.List(client, serverID).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	return attachinterfaces.ExtractInterfaces(pages)
}

// newAttachOpts builds the attachment body, refusing --tag when the client is
// pinned below the microversion that added it.
func newAttachOpts(client *gophercloud.ServiceClient, create attachinterfaces.CreateOpts, tag string) (attachinterfaces.CreateOptsBuilder, error) {
	if tag == "" {
		return create, nil
	}
	if !computeSupportsMicroversion(client, interfaceTagMicroversion) {
		return nil, fmt.Errorf("--tag requires nova microversion %s or later; this client is pinned to %s",
			interfaceTagMicroversion, client.Microversion)
	}
	return attachOpts{CreateOpts: create, Tag: tag}, nil
}

// resolveNetworkResource derives a neutron client from the same session and
// resolves a port or network reference to an ID.
func resolveNetworkResource(ctx context.Context, ac *auth.Client, ref string,
	fn func(context.Context, *gophercloud.ServiceClient, string) (string, error),
) (string, error) {
	networkClient, err := ac.Network()
	if err != nil {
		return "", err
	}
	return fn(ctx, networkClient, ref)
}

func writeInterface(o *output.Options, w io.Writer, iface *attachinterfaces.Interface) error {
	addresses := make([]string, 0, len(iface.FixedIPs))
	for _, fixed := range iface.FixedIPs {
		addresses = append(addresses, fixed.IPAddress)
	}
	return o.WriteSingle(w,
		[]string{"port_id", "net_id", "mac_addr", "port_state", "fixed_ips"},
		[]any{iface.PortID, iface.NetID, iface.MACAddr, iface.PortState, addresses})
}
