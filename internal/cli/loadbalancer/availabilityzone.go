package loadbalancer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/nameflag"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Octavia availability zones and availability zone profiles
// (/v2/lbaas/availabilityzones, /v2/lbaas/availabilityzoneprofiles, octavia
// Ussuri and later, so on every supported cloud). gophercloud v2 has no
// package for either, so these verbs use raw requests decoding into the DTOs
// below — the raw fallback per AGENTS.md; replace them with the typed calls
// if gophercloud grows them. Command and flag names mirror python-octaviaclient
// 3.15.0's availabilityzone.py and availabilityzoneprofile.py, UNVERIFIED
// against KeyStack (see the package doc).
//
// An availability zone is keyed by its name — octavia gives it no ID — while
// a profile has an ID and a non-unique name.

const (
	azCollection        = "availabilityzones"
	azProfileCollection = "availabilityzoneprofiles"
	flagAZProfile       = "availabilityzoneprofile"
	flagAZData          = "availability-zone-data"
)

type azProfile struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	ProviderName         string `json:"provider_name"`
	AvailabilityZoneData string `json:"availability_zone_data"`
}

type availabilityZone struct {
	Name                      string `json:"name"`
	Description               string `json:"description"`
	Enabled                   bool   `json:"enabled"`
	AvailabilityZoneProfileID string `json:"availability_zone_profile_id"`
}

func azProfileFields(p *azProfile) ([]string, []any) {
	return []string{"id", "name", "provider_name", "availability_zone_data"},
		[]any{p.ID, p.Name, p.ProviderName, p.AvailabilityZoneData}
}

func azFields(z *availabilityZone) ([]string, []any) {
	return []string{"name", "availability_zone_profile_id", "enabled", "description"},
		[]any{z.Name, z.AvailabilityZoneProfileID, z.Enabled, z.Description}
}

var (
	azProfileColumns = fieldNames(azProfileFields(&azProfile{}))
	azColumns        = fieldNames(azFields(&availabilityZone{}))
)

// --- raw transport -------------------------------------------------------------

// listRaw GETs an octavia collection and follows its "<key>_links" next links
// (octavia spells the profile one "availability_zone_profile_links"), standing
// in for the Pager gophercloud would provide.
func listRaw[T any](ctx context.Context, client *gophercloud.ServiceClient, collection, key string, query url.Values) ([]T, error) {
	next := client.ServiceURL("lbaas", collection)
	if len(query) > 0 {
		next += "?" + query.Encode()
	}
	var all []T
	for next != "" {
		var body map[string]json.RawMessage
		resp, err := client.Get(ctx, next, &body, nil)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
			return nil, err
		}
		var page []T
		if err := json.Unmarshal(body[key], &page); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", key, err)
		}
		all = append(all, page...)
		next = ""
		for k, raw := range body {
			if !strings.HasSuffix(k, "_links") {
				continue
			}
			var links []struct{ Href, Rel string }
			if json.Unmarshal(raw, &links) == nil {
				for _, l := range links {
					if l.Rel == "next" {
						next = l.Href
					}
				}
			}
		}
	}
	return all, nil
}

// writeRaw POSTs or PUTs {envelope: body} and decodes the envelope's record.
func writeRaw(ctx context.Context, client *gophercloud.ServiceClient, method, u, envelope string, body map[string]any, into any) error {
	var resp map[string]json.RawMessage
	opts := &gophercloud.RequestOpts{
		JSONBody:     map[string]any{envelope: body},
		JSONResponse: &resp,
		OkCodes:      []int{200, 201, 202},
	}
	r, err := client.Request(ctx, method, u, opts)
	if r != nil {
		defer func() { _ = r.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(r, err); err != nil {
		return err
	}
	if err := json.Unmarshal(resp[envelope], into); err != nil {
		return fmt.Errorf("decoding %s: %w", envelope, err)
	}
	return nil
}

func getRaw(ctx context.Context, client *gophercloud.ServiceClient, u, envelope string, into any) error {
	rec, err := getRecord(ctx, client, u, envelope)
	if err != nil {
		return err
	}
	return json.Unmarshal(rec, into)
}

func deleteRaw(ctx context.Context, client *gophercloud.ServiceClient, u string) error {
	r, err := client.Delete(ctx, u, &gophercloud.RequestOpts{OkCodes: []int{204}})
	if r != nil {
		defer func() { _ = r.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(r, err)
	return err
}

func azURL(client *gophercloud.ServiceClient, name string) string {
	return client.ServiceURL("lbaas", azCollection, url.PathEscape(name))
}

func azProfileURL(client *gophercloud.ServiceClient, id string) string {
	return client.ServiceURL("lbaas", azProfileCollection, id)
}

func resolveAZProfileID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	return resolveByName("availability zone profile", ref, func() ([]string, error) {
		all, err := listRaw[azProfile](ctx, client, azProfileCollection, "availability_zone_profiles", url.Values{"name": {ref}})
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(all))
		for _, p := range all {
			ids = append(ids, p.ID)
		}
		return ids, nil
	})
}

// --- availabilityzoneprofile -----------------------------------------------------

func newAZProfileCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "availabilityzoneprofile",
		Short: "Manage availability zone profiles (provider driver + zone data)",
	}
	cmd.AddCommand(
		newAZProfileListCommand(a, o),
		newAZProfileShowCommand(a, o),
		newAZProfileCreateCommand(a, o),
		newAZProfileSetCommand(a, o),
		newAZProfileDeleteCommand(a, o),
	)
	return cmd
}

func newAZProfileListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name, provider string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List availability zone profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAZProfileList(ctx, client, o, name, provider, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "filter availability zone profiles by name")
	cmd.Flags().StringVar(&provider, "provider", "", "filter by provider driver, e.g. amphora")
	return cmd
}

func runAZProfileList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name, provider string, w io.Writer) error {
	q := url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	if provider != "" {
		q.Set("provider_name", provider)
	}
	all, err := listRaw[azProfile](ctx, client, azProfileCollection, "availability_zone_profiles", q)
	if err != nil {
		return fmt.Errorf("listing availability zone profiles: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Provider Name"}, Rows: make([][]any, 0, len(all))}
	for _, p := range all {
		t.Rows = append(t.Rows, []any{p.ID, p.Name, p.ProviderName})
	}
	return o.WriteList(w, t)
}

func newAZProfileShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <availabilityzoneprofile>",
		Short: "Show availability zone profile details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAZProfileShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runAZProfileShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveAZProfileID(ctx, client, ref)
	if err != nil {
		return err
	}
	var p azProfile
	if err := getRaw(ctx, client, azProfileURL(client, id), "availability_zone_profile", &p); err != nil {
		return fmt.Errorf("showing availability zone profile %q: %w", ref, err)
	}
	fields, values := azProfileFields(&p)
	return o.WriteSingle(w, fields, values)
}

func newAZProfileCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var flagName, provider, data string
	cmd := &cobra.Command{
		Use:   "create [<name>]",
		Short: "Create an availability zone profile",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			name, err := nameflag.Resolve(args, flagName, true)
			if err != nil {
				return err
			}
			if provider == "" || data == "" {
				return fmt.Errorf("--provider and --%s are required", flagAZData)
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAZProfileCreate(ctx, client, o, name, provider, data, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	// Upstream spells the name --name only; the positional is koc's, as for
	// flavorprofile create.
	fl.StringVar(&flagName, "name", "", "name of the availability zone profile (upstream spelling; the positional form also works)")
	fl.StringVar(&provider, "provider", "", "provider driver the profile targets, e.g. amphora (required)")
	fl.StringVar(&data, flagAZData, "", "driver-specific JSON, e.g. '{\"compute_zone\": \"az1\"}' (required)")
	return cmd
}

func runAZProfileCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name, provider, data string, w io.Writer,
) error {
	if err := o.CheckColumns(azProfileColumns...); err != nil {
		return err
	}
	var p azProfile
	body := map[string]any{"name": name, "provider_name": provider, "availability_zone_data": data}
	if err := writeRaw(ctx, client, http.MethodPost, client.ServiceURL("lbaas", azProfileCollection),
		"availability_zone_profile", body, &p); err != nil {
		return fmt.Errorf("creating availability zone profile %q: %w", name, err)
	}
	fields, values := azProfileFields(&p)
	return o.WriteSingle(w, fields, values)
}

func newAZProfileSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name, provider, data string
	cmd := &cobra.Command{
		Use:   "set <availabilityzoneprofile>",
		Short: "Update an availability zone profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			changed := changedFlags(cmd.Flags())
			body := map[string]any{}
			if changed["name"] {
				body["name"] = name
			}
			if changed["provider"] {
				body["provider_name"] = provider
			}
			if changed[flagAZData] {
				body["availability_zone_data"] = data
			}
			if len(body) == 0 {
				return fmt.Errorf("nothing to set: pass --name, --provider and/or --%s", flagAZData)
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAZProfileSet(ctx, client, o, args[0], body, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "new profile name")
	fl.StringVar(&provider, "provider", "", "new provider driver")
	fl.StringVar(&data, flagAZData, "", "new driver-specific JSON")
	return cmd
}

func runAZProfileSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, body map[string]any, w io.Writer,
) error {
	if err := o.CheckColumns(azProfileColumns...); err != nil {
		return err
	}
	id, err := resolveAZProfileID(ctx, client, ref)
	if err != nil {
		return err
	}
	var p azProfile
	if err := writeRaw(ctx, client, http.MethodPut, azProfileURL(client, id), "availability_zone_profile", body, &p); err != nil {
		return fmt.Errorf("updating availability zone profile %q: %w", ref, err)
	}
	fields, values := azProfileFields(&p)
	return o.WriteSingle(w, fields, values)
}

func newAZProfileDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <availabilityzoneprofile> [<availabilityzoneprofile>...]",
		Short: "Delete one or more availability zone profiles",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAZProfileDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runAZProfileDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveAZProfileID(ctx, client, ref)
		if err != nil {
			return err
		}
		if derr := deleteRaw(ctx, client, azProfileURL(client, id)); derr != nil {
			return fmt.Errorf("deleting availability zone profile %q: %w", ref, derr)
		}
		if _, werr := fmt.Fprintf(w, "Deleted availability zone profile %s\n", ref); werr != nil {
			return werr
		}
		return nil
	})
}

// --- availabilityzone -------------------------------------------------------------

func newAZCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "availabilityzone",
		Short: "Manage load balancer availability zones",
	}
	cmd.AddCommand(
		newAZListCommand(a, o),
		newAZShowCommand(a, o),
		newAZCreateCommand(a, o),
		newAZSetCommand(a, o),
		newAZUnsetCommand(a, o),
		newAZDeleteCommand(a, o),
	)
	return cmd
}

type azListFlags struct {
	name, profile   string
	enable, disable bool
}

func newAZListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &azListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List load balancer availability zones",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAZList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "filter availability zones by name")
	fl.StringVar(&f.profile, flagAZProfile, "", "filter by availability zone profile (name or ID)")
	fl.BoolVar(&f.enable, "enable", false, "list only enabled availability zones")
	fl.BoolVar(&f.disable, "disable", false, "list only disabled availability zones")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	return cmd
}

func runAZList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *azListFlags, w io.Writer) error {
	q := url.Values{}
	if f.name != "" {
		q.Set("name", f.name)
	}
	if f.profile != "" {
		id, err := resolveAZProfileID(ctx, client, f.profile)
		if err != nil {
			return err
		}
		q.Set("availability_zone_profile_id", id)
	}
	switch {
	case f.enable:
		q.Set("enabled", "true")
	case f.disable:
		q.Set("enabled", "false")
	}
	all, err := listRaw[availabilityZone](ctx, client, azCollection, "availability_zones", q)
	if err != nil {
		return fmt.Errorf("listing availability zones: %w", err)
	}
	t := output.Table{Columns: []string{"Name", "Availability Zone Profile ID", "Enabled"}, Rows: make([][]any, 0, len(all))}
	for _, z := range all {
		t.Rows = append(t.Rows, []any{z.Name, z.AvailabilityZoneProfileID, z.Enabled})
	}
	return o.WriteList(w, t)
}

func newAZShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <availabilityzone>",
		Short: "Show availability zone details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAZShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runAZShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, w io.Writer) error {
	var z availabilityZone
	if err := getRaw(ctx, client, azURL(client, name), "availability_zone", &z); err != nil {
		return fmt.Errorf("showing availability zone %q: %w", name, err)
	}
	fields, values := azFields(&z)
	return o.WriteSingle(w, fields, values)
}

type azCreateFlags struct {
	name, profile, description string
	enable, disable            bool
}

func newAZCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &azCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create [<name>]",
		Short: "Create a load balancer availability zone",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			name, err := nameflag.Resolve(args, f.name, true)
			if err != nil {
				return err
			}
			if f.profile == "" {
				return fmt.Errorf("--%s is required", flagAZProfile)
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAZCreate(ctx, client, o, name, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "name of the availability zone (upstream spelling; the positional form also works)")
	fl.StringVar(&f.profile, flagAZProfile, "", "availability zone profile to base the zone on (name or ID, required)")
	fl.StringVar(&f.description, "description", "", "availability zone description")
	fl.BoolVar(&f.enable, "enable", false, "create the availability zone enabled (the default)")
	fl.BoolVar(&f.disable, "disable", false, "create the availability zone disabled, so it cannot be selected yet")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	return cmd
}

func runAZCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *azCreateFlags, w io.Writer,
) error {
	if err := o.CheckColumns(azColumns...); err != nil {
		return err
	}
	profileID, err := resolveAZProfileID(ctx, client, f.profile)
	if err != nil {
		return err
	}
	body := map[string]any{"name": name, "availability_zone_profile_id": profileID}
	if f.description != "" {
		body["description"] = f.description
	}
	if f.disable {
		body["enabled"] = false
	}
	var z availabilityZone
	if err := writeRaw(ctx, client, http.MethodPost, client.ServiceURL("lbaas", azCollection), "availability_zone", body, &z); err != nil {
		return fmt.Errorf("creating availability zone %q: %w", name, err)
	}
	fields, values := azFields(&z)
	return o.WriteSingle(w, fields, values)
}

func newAZSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var description string
	var enable, disable bool
	cmd := &cobra.Command{
		Use:   "set <availabilityzone>",
		Short: "Update a load balancer availability zone",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			changed := changedFlags(cmd.Flags())
			body := map[string]any{}
			if changed["description"] {
				body["description"] = description
			}
			switch {
			case enable:
				body["enabled"] = true
			case disable:
				body["enabled"] = false
			}
			if len(body) == 0 {
				return fmt.Errorf("nothing to set: pass --description, --enable or --disable")
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAZSet(ctx, client, o, args[0], body, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&description, "description", "", "new availability zone description")
	fl.BoolVar(&enable, "enable", false, "allow the availability zone to be selected")
	fl.BoolVar(&disable, "disable", false, "stop the availability zone from being selected")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	return cmd
}

func runAZSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, body map[string]any, w io.Writer,
) error {
	if err := o.CheckColumns(azColumns...); err != nil {
		return err
	}
	var z availabilityZone
	if err := writeRaw(ctx, client, http.MethodPut, azURL(client, name), "availability_zone", body, &z); err != nil {
		return fmt.Errorf("updating availability zone %q: %w", name, err)
	}
	fields, values := azFields(&z)
	return o.WriteSingle(w, fields, values)
}

var azUnsetSpec = &unsetSpec{
	kind: "availability zone", envelope: "availability_zone",
	attrs: []unsetAttr{
		{"description", "description", clearHelp("availability zone description")},
	},
	columns: azColumns,
	render:  renderRecord(azFields),
}

func newAZUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newUnsetCommand(a, o, azUnsetSpec, "unset <availabilityzone>", 1,
		func(_ context.Context, _ *gophercloud.ServiceClient, args []string) (unsetTarget, error) {
			return unsetTarget{path: []string{"lbaas", azCollection, url.PathEscape(args[0])}, ref: args[0]}, nil
		})
}

func newAZDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <availabilityzone> [<availabilityzone>...]",
		Short: "Delete one or more load balancer availability zones",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newLoadBalancerClient(ctx, a)
			if err != nil {
				return err
			}
			return runAZDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runAZDelete(ctx context.Context, client *gophercloud.ServiceClient, names []string, w io.Writer) error {
	return batchdelete.Each(names, func(name string) error {
		if err := deleteRaw(ctx, client, azURL(client, name)); err != nil {
			return fmt.Errorf("deleting availability zone %q: %w", name, err)
		}
		if _, werr := fmt.Fprintf(w, "Deleted availability zone %s\n", name); werr != nil {
			return werr
		}
		return nil
	})
}
