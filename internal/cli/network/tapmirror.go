package network

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/taas/tapmirrors"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "tap mirror ..." mirrors upstream network/v2/taas/tap_mirror.py (TaaS GRE /
// ERSPAN v1 mirrors). Only the mirror noun has a gophercloud package; "tap
// service" and "tap flow" are not implemented. Upstream's modifying verb is
// "update", not "set", and koc keeps that word.

// newTapCommands builds the "tap" top-level noun.
func newTapCommands(a *auth.Options, o *output.Options) []*cobra.Command {
	tap := &cobra.Command{
		Use:   "tap",
		Short: "Manage tap-as-a-service mirrors (requires the tap-mirror extension)",
	}
	mirror := &cobra.Command{
		Use:   "mirror",
		Short: "Manage tap mirrors (requires the tap-mirror extension)",
	}
	mirror.AddCommand(
		newTapMirrorCreateCommand(a, o),
		newTapMirrorDeleteCommand(a, o),
		newTapMirrorListCommand(a, o),
		newTapMirrorShowCommand(a, o),
		newTapMirrorUpdateCommand(a, o),
	)
	tap.AddCommand(mirror)
	return []*cobra.Command{tap}
}

// tapMirror is what koc decodes a mirror into. gophercloud's
// tapmirrors.Directions types the tunnel IDs as JSON strings holding integers,
// but neutron-lib's DIRECTION_SPEC validates them as integers and neutron
// echoes what it was sent, so a number there would fail to decode; a plain map
// takes either.
type tapMirror struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	TenantID    string         `json:"tenant_id"`
	ProjectID   string         `json:"project_id"`
	PortID      string         `json:"port_id"`
	Directions  map[string]any `json:"directions"`
	RemoteIP    string         `json:"remote_ip"`
	MirrorType  string         `json:"mirror_type"`
}

func extractTapMirror(r interface{ ExtractInto(any) error }) (*tapMirror, error) {
	var s struct {
		TapMirror *tapMirror `json:"tap_mirror"`
	}
	if err := r.ExtractInto(&s); err != nil {
		return nil, err
	}
	if s.TapMirror == nil {
		return nil, fmt.Errorf("response carries no tap_mirror")
	}
	return s.TapMirror, nil
}

// tapMirrorPage replaces tapmirrors.TapMirrorPage, whose IsEmpty decodes
// through the typed Directions and so fails on a list with numeric tunnel IDs
// before koc ever sees the page.
type tapMirrorPage struct {
	pagination.LinkedPageBase
}

func (p tapMirrorPage) NextPageURL() (string, error) {
	var s struct {
		Links []gophercloud.Link `json:"tap_mirrors_links"`
	}
	if err := p.ExtractInto(&s); err != nil {
		return "", err
	}
	return gophercloud.ExtractNextURL(s.Links)
}

func (p tapMirrorPage) IsEmpty() (bool, error) {
	if p.StatusCode == http.StatusNoContent {
		return true, nil
	}
	all, err := extractTapMirrors(p)
	return len(all) == 0, err
}

// listTapMirrors is tapmirrors.List over tapMirrorPage; the query still comes
// from the typed ListOpts.
func listTapMirrors(client *gophercloud.ServiceClient, opts tapmirrors.ListOpts) pagination.Pager {
	q, err := opts.ToTapMirrorListQuery()
	if err != nil {
		return pagination.Pager{Err: err}
	}
	return pagination.NewPager(client, client.ServiceURL("taas", "tap_mirrors")+q, func(r pagination.PageResult) pagination.Page {
		return tapMirrorPage{pagination.LinkedPageBase{PageResult: r}}
	})
}

func extractTapMirrors(page pagination.Page) ([]tapMirror, error) {
	p, ok := page.(tapMirrorPage)
	if !ok {
		return nil, fmt.Errorf("unexpected tap mirror page type %T", page)
	}
	var all []tapMirror
	err := p.ExtractIntoSlicePtr(&all, "tap_mirrors")
	return all, err
}

// tenant is upstream's "Tenant" column (the tenant_id key), falling back to
// project_id for a neutron that returns only the newer name.
func (m *tapMirror) tenant() string {
	if m.TenantID != "" {
		return m.TenantID
	}
	return m.ProjectID
}

// directions renders an absent map as empty rather than as JSON "null".
func (m *tapMirror) directions() any {
	if len(m.Directions) == 0 {
		return nil
	}
	return m.Directions
}

func tapMirrorShowFields(m *tapMirror) ([]string, []any) {
	fields := []string{"id", "name", "description", "port_id", "directions", "remote_ip", "mirror_type", "project_id"}
	values := []any{m.ID, m.Name, m.Description, m.PortID, m.directions(), m.RemoteIP, m.MirrorType, m.ProjectID}
	return fields, values
}

func resolveTapMirrorID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	id, err := resolveByName(client, "tap mirror", nameOrID, func(c *gophercloud.ServiceClient) ([]tapMirror, error) {
		pages, err := listTapMirrors(c, tapmirrors.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return extractTapMirrors(pages)
	}, func(m tapMirror) string { return m.ID })
	return id, explainMissingService(ctx, client, err, extTapMirror)
}

// parseTapMirrorDirections folds repeated --directions values the way
// upstream's JSONKeyValueAction does: each is a JSON object merged in, or
// else a single key=value pair (whose value stays a string, as upstream's).
func parseTapMirrorDirections(specs []string) (map[string]any, error) {
	out := map[string]any{}
	for _, spec := range specs {
		var obj map[string]any
		if err := json.Unmarshal([]byte(spec), &obj); err == nil {
			for k, v := range obj {
				out[k] = v
			}
			continue
		}
		k, v, ok := strings.Cut(spec, "=")
		if !ok {
			return nil, fmt.Errorf("--directions: expected <direction>=<tunnel-id> or a JSON object, got %q", spec)
		}
		out[k] = v
	}
	return out, nil
}

type tapMirrorCreateFlags struct {
	name          string
	description   string
	port          string
	directions    []string
	remoteIP      string
	mirrorType    string
	project       string
	projectDomain string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID string
}

func newTapMirrorCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &tapMirrorCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a tap mirror",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.projectID, err = resolveProjectRef(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			return runTapMirrorCreate(ctx, client, o, f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "name of the tap mirror")
	fl.StringVar(&f.description, flagDescription, "", "description of the tap mirror")
	fl.StringVar(&f.port, "port", "", "port the tap mirror is connected to (name or ID)")
	fl.StringArrayVar(&f.directions, "directions", nil,
		"direction and tunnel ID, as IN=<id>, OUT=<id> or a JSON object (repeatable)")
	fl.StringVar(&f.remoteIP, "remote-ip", "", "remote end of the GRE or ERSPAN v1 tunnel")
	fl.StringVar(&f.mirrorType, "mirror-type", "", "mirror type (gre, erspanv1)")
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID; admin)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	for _, req := range []string{"port", "directions", "remote-ip", "mirror-type"} {
		_ = cmd.MarkFlagRequired(req)
	}
	return cmd
}

func runTapMirrorCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *tapMirrorCreateFlags, flags flagSet, w io.Writer) error {
	directions, err := parseTapMirrorDirections(f.directions)
	if err != nil {
		return err
	}
	portID, err := resolvePortID(ctx, client, f.port)
	if err != nil {
		return err
	}
	attrs := map[string]any{
		"port_id":     portID,
		"directions":  directions,
		"remote_ip":   f.remoteIP,
		"mirror_type": f.mirrorType,
	}
	if flags.Changed("name") {
		attrs["name"] = f.name
	}
	if flags.Changed(flagDescription) {
		attrs["description"] = f.description
	}
	if f.projectID != "" {
		attrs["project_id"] = f.projectID
	}
	m, err := extractTapMirror(tapmirrors.Create(ctx, client, bgptapBody{key: "tap_mirror", attrs: attrs}))
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("creating tap mirror: %w", err), extTapMirror)
	}
	fields, values := tapMirrorShowFields(m)
	return o.WriteSingle(w, fields, values)
}

func newTapMirrorDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <tap-mirror> [<tap-mirror> ...]",
		Short: "Delete tap mirror(s)",
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
			return runTapMirrorDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runTapMirrorDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveTapMirrorID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := tapmirrors.Delete(ctx, client, id).ExtractErr(); err != nil {
			return explainMissingService(ctx, client, fmt.Errorf("deleting tap mirror %s: %w", ref, err), extTapMirror)
		}
		_, err = fmt.Fprintf(w, "Deleted tap mirror %s\n", ref)
		return err
	})
}

type tapMirrorListFlags struct {
	project       string
	projectDomain string
}

func newTapMirrorListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &tapMirrorListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tap mirrors",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRef(ctx, session, f.project, f.projectDomain)
			if err != nil {
				return err
			}
			return runTapMirrorList(ctx, client, o, projectID, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.project, flagProject, "", "list only tap mirrors owned by this project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
	return cmd
}

// tapMirrorListColumns are upstream's: ListTapMirror always renders the long
// listing, so there is no --long.
var tapMirrorListColumns = []string{"ID", "Tenant", "Name", "Port", "Directions", "Remote IP", "Mirror Type"}

func runTapMirrorList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, projectID string, w io.Writer) error {
	pages, err := listTapMirrors(client, tapmirrors.ListOpts{ProjectID: projectID}).AllPages(ctx)
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("listing tap mirrors: %w", err), extTapMirror)
	}
	all, err := extractTapMirrors(pages)
	if err != nil {
		return fmt.Errorf("parsing tap mirror list: %w", err)
	}
	t := output.Table{Columns: tapMirrorListColumns, Rows: make([][]any, 0, len(all))}
	for i := range all {
		m := &all[i]
		t.Rows = append(t.Rows, []any{m.ID, m.tenant(), m.Name, m.PortID, m.directions(), m.RemoteIP, m.MirrorType})
	}
	return o.WriteList(w, t)
}

func newTapMirrorShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <tap-mirror>",
		Short: "Show details of a tap mirror",
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
			return runTapMirrorShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runTapMirrorShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveTapMirrorID(ctx, client, ref)
	if err != nil {
		return err
	}
	m, err := extractTapMirror(tapmirrors.Get(ctx, client, id))
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("getting tap mirror %s: %w", ref, err), extTapMirror)
	}
	fields, values := tapMirrorShowFields(m)
	return o.WriteSingle(w, fields, values)
}

type tapMirrorUpdateFlags struct {
	name        string
	description string
}

func newTapMirrorUpdateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &tapMirrorUpdateFlags{}
	cmd := &cobra.Command{
		Use:   "update <tap-mirror>",
		Short: "Update a tap mirror",
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
			return runTapMirrorUpdate(ctx, client, o, args[0], f, cmd.Flags(), cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "new name for the tap mirror")
	fl.StringVar(&f.description, flagDescription, "", "new description for the tap mirror")
	return cmd
}

// runTapMirrorUpdate PUTs name and description, the only attributes neutron
// lets a mirror change.
func runTapMirrorUpdate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *tapMirrorUpdateFlags, flags flagSet, w io.Writer) error {
	var opts tapmirrors.UpdateOpts
	if flags.Changed("name") {
		opts.Name = &f.name
	}
	if flags.Changed(flagDescription) {
		opts.Description = &f.description
	}
	if opts.Name == nil && opts.Description == nil {
		return fmt.Errorf("tap mirror update requires --name or --description")
	}
	id, err := resolveTapMirrorID(ctx, client, ref)
	if err != nil {
		return err
	}
	m, err := extractTapMirror(tapmirrors.Update(ctx, client, id, opts))
	if err != nil {
		return explainMissingService(ctx, client, fmt.Errorf("updating tap mirror %s: %w", ref, err), extTapMirror)
	}
	fields, values := tapMirrorShowFields(m)
	return o.WriteSingle(w, fields, values)
}
