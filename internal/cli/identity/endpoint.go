package identity

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/endpoints"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/services"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names follow upstream OSC (`openstack endpoint ...`). UNVERIFIED against
// KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.

func newEndpointCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "endpoint", Short: "Manage service catalog endpoints"}
	cmd.AddCommand(
		newEndpointListCommand(a, o),
		newEndpointShowCommand(a, o),
		newEndpointCreateCommand(a, o),
		newEndpointDeleteCommand(a, o),
		newEndpointSetCommand(a, o),
	)
	return cmd
}

func availability(iface string) (gophercloud.Availability, error) {
	switch iface {
	case "public":
		return gophercloud.AvailabilityPublic, nil
	case "internal":
		return gophercloud.AvailabilityInternal, nil
	case "admin":
		return gophercloud.AvailabilityAdmin, nil
	case "":
		return "", nil
	default:
		return "", fmt.Errorf("invalid interface %q: must be public, internal or admin", iface)
	}
}

func newEndpointListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var service, iface, region string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List endpoints",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runEndpointList(ctx, client, o, service, iface, region, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&service, "service", "", "filter by service (name or ID)")
	fl.StringVar(&iface, "interface", "", "filter by interface: public, internal or admin")
	fl.StringVar(&region, "region", "", "filter by region ID")
	return cmd
}

func runEndpointList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, service, iface, region string, w io.Writer) error {
	avail, err := availability(iface)
	if err != nil {
		return err
	}
	serviceID, err := resolveServiceID(ctx, client, service)
	if err != nil {
		return err
	}
	pages, err := endpoints.List(client, endpoints.ListOpts{Availability: avail, ServiceID: serviceID, RegionID: region}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing endpoints: %w", err)
	}
	all, err := endpoints.ExtractEndpoints(pages)
	if err != nil {
		return fmt.Errorf("parsing endpoint list: %w", err)
	}
	// Upstream `openstack endpoint list` renders the service name and type, not
	// the raw service_id the endpoints API returns, so resolve them from the
	// catalog with a single /services list keyed by ID.
	catalog, err := serviceCatalog(ctx, client)
	if err != nil {
		return fmt.Errorf("listing services: %w", err)
	}
	// Column order mirrors upstream OSC: Service Name/Type after Region, then
	// Enabled before Interface.
	t := output.Table{Columns: []string{"ID", "Region", "Service Name", "Service Type", "Enabled", "Interface", "URL"}, Rows: make([][]any, 0, len(all))}
	for _, e := range all {
		svc := catalog[e.ServiceID]
		t.Rows = append(t.Rows, []any{e.ID, e.Region, svc.name, svc.typ, e.Enabled, string(e.Availability), e.URL})
	}
	return o.WriteList(w, t)
}

// serviceInfo carries the human-facing name and type of a catalog service.
type serviceInfo struct {
	name string
	typ  string
}

// serviceCatalog lists the identity catalog once and indexes it by service ID
// so endpoint rows can be enriched with the service name and type the way
// upstream OSC does. A service_id with no matching entry (e.g. a deleted
// service) yields the zero serviceInfo, i.e. blank cells, matching OSC.
func serviceCatalog(ctx context.Context, client *gophercloud.ServiceClient) (map[string]serviceInfo, error) {
	pages, err := services.List(client, services.ListOpts{}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	all, err := services.ExtractServices(pages)
	if err != nil {
		return nil, err
	}
	m := make(map[string]serviceInfo, len(all))
	for _, s := range all {
		m[s.ID] = serviceInfo{name: s.Name, typ: s.Type}
	}
	return m, nil
}

func newEndpointShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <endpoint-id>",
		Short: "Show endpoint details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runEndpointShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runEndpointShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	e, err := endpoints.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing endpoint %q: %w", id, err)
	}
	// Enrich with the service name and type like upstream OSC; a lookup failure
	// (e.g. the service was deleted) leaves them blank rather than failing show.
	var svcName, svcType string
	if s, err := services.Get(ctx, client, e.ServiceID).Extract(); err == nil {
		svcName, svcType = s.Name, s.Type
	}
	return o.WriteSingle(w,
		[]string{"ID", "Region", "Service ID", "Service Name", "Service Type", "Interface", "Enabled", "URL", "Description"},
		[]any{e.ID, e.Region, e.ServiceID, svcName, svcType, string(e.Availability), e.Enabled, e.URL, e.Description})
}

type endpointWriteFlags struct {
	region      string
	description string
	service     string
	iface       string
	url         string
	enable      bool
	enableSet   bool
	disableSet  bool
}

func newEndpointCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &endpointWriteFlags{}
	cmd := &cobra.Command{
		Use:   "create <service> <interface> <url>",
		Short: "Create a new endpoint",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.enableSet = cmd.Flags().Changed("enable")
			f.disableSet = cmd.Flags().Changed("disable")
			if err := checkEnableDisable(f.enableSet, f.disableSet); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runEndpointCreate(ctx, client, o, args[0], args[1], args[2], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.region, "region", "", "region the endpoint is located in")
	fl.StringVar(&f.description, "description", "", "endpoint description")
	fl.BoolVar(&f.enable, "enable", true, "enable the endpoint (default)")
	fl.BoolVar(new(bool), "disable", false, "disable the endpoint")
	return cmd
}

func runEndpointCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, service, iface, url string, f *endpointWriteFlags, w io.Writer) error {
	avail, err := availability(iface)
	if err != nil {
		return err
	}
	serviceID, err := resolveServiceID(ctx, client, service)
	if err != nil {
		return err
	}
	opts := endpoints.CreateOpts{
		Availability: avail,
		URL:          url,
		ServiceID:    serviceID,
		Region:       f.region,
		Description:  f.description,
		Enabled:      enabledFromFlags(f.enableSet, f.disableSet, f.enable),
	}
	e, err := endpoints.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating endpoint: %w", err)
	}
	return o.WriteSingle(w,
		[]string{"ID", "Region", "Service ID", "Interface", "Enabled", "URL"},
		[]any{e.ID, e.Region, e.ServiceID, string(e.Availability), e.Enabled, e.URL})
}

func newEndpointDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <endpoint-id>",
		Short: "Delete an endpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runEndpointDelete(ctx, client, args[0])
		},
	}
}

func runEndpointDelete(ctx context.Context, client *gophercloud.ServiceClient, id string) error {
	if err := endpoints.Delete(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting endpoint %q: %w", id, err)
	}
	return nil
}

func newEndpointSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &endpointWriteFlags{}
	cmd := &cobra.Command{
		Use:   "set <endpoint-id>",
		Short: "Update an endpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.enableSet = cmd.Flags().Changed("enable")
			f.disableSet = cmd.Flags().Changed("disable")
			if err := checkEnableDisable(f.enableSet, f.disableSet); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runEndpointSet(ctx, client, args[0], f)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.service, "service", "", "new service (name or ID)")
	fl.StringVar(&f.iface, "interface", "", "new interface: public, internal or admin")
	fl.StringVar(&f.url, "url", "", "new endpoint URL")
	fl.StringVar(&f.region, "region", "", "new region")
	fl.StringVar(&f.description, "description", "", "new description")
	fl.BoolVar(&f.enable, "enable", false, "enable the endpoint")
	fl.BoolVar(new(bool), "disable", false, "disable the endpoint")
	return cmd
}

func runEndpointSet(ctx context.Context, client *gophercloud.ServiceClient, id string, f *endpointWriteFlags) error {
	avail, err := availability(f.iface)
	if err != nil {
		return err
	}
	serviceID, err := resolveServiceID(ctx, client, f.service)
	if err != nil {
		return err
	}
	opts := endpoints.UpdateOpts{
		Availability: avail,
		URL:          f.url,
		ServiceID:    serviceID,
		Region:       f.region,
		Description:  f.description,
		Enabled:      enabledFromFlags(f.enableSet, f.disableSet, f.enable),
	}
	if _, err := endpoints.Update(ctx, client, id, opts).Extract(); err != nil {
		return fmt.Errorf("updating endpoint %q: %w", id, err)
	}
	return nil
}
