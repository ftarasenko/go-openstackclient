package crossservice

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// `extension list` and `extension show` are upstream's openstack.common
// spelling (openstackclient/common/extension.py). The list merges each
// service's extension discovery document — nova's GET /extensions (still
// served, a static list, as of nova 34), cinder's GET /extensions and
// neutron's GET /v2.0/extensions — and, like upstream, a service that cannot
// answer is a warning rather than a failure. Keystone v3 has no extensions
// API; upstream lists nothing for it there, and so does koc. `show` is
// neutron's only, as upstream's is.
//
// gophercloud has no package for any of the three discovery endpoints, so
// they are one raw GET each (extensionsGet).
//
// `koc network extension list/show` stay as the neutron-only spelling.

// extension is the shape all three services share.
type extension struct {
	Name        string `json:"name"`
	Alias       string `json:"alias"`
	Description string `json:"description"`
	Namespace   string `json:"namespace"`
	Updated     string `json:"updated"`
	Links       []any  `json:"links"`
}

// clientFunc lazily builds one service's client; an error means the
// service is not available to this session (no catalog entry, --creds-from-ns).
type clientFunc func() (*gophercloud.ServiceClient, error)

// extensionServices are the clients `extension list` may query.
type extensionServices struct {
	compute, volume, network clientFunc
}

type extensionListFlags struct {
	compute, identity, network, volume, long bool
}

func newExtensionCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "extension", Short: "List and show API extensions"}
	cmd.AddCommand(newExtensionListCommand(a, o), newExtensionShowCommand(a, o))
	return cmd
}

func newExtensionListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &extensionListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List API extensions (compute, volume and network; all of them without a service flag)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := a.NewSession(ctx)
			if err != nil {
				return err
			}
			svc := extensionServices{compute: client.Compute, volume: client.Volume, network: client.Network}
			return runExtensionList(ctx, svc, o, f, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.compute, "compute", false, "list extensions for the Compute API")
	fl.BoolVar(&f.identity, "identity", false, "list extensions for the Identity API (only v2 had any; koc speaks v3)")
	fl.BoolVar(&f.network, "network", false, "list extensions for the Network API")
	fl.BoolVar(&f.volume, "volume", false, "list extensions for the Block Storage API")
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	return cmd
}

func runExtensionList(ctx context.Context, svc extensionServices, o *output.Options,
	f *extensionListFlags, w, errw io.Writer,
) error {
	cols := []string{"Name", "Alias", "Description"}
	if f.long {
		cols = append(cols, "Namespace", "Updated At", "Links")
	}
	all := !f.compute && !f.identity && !f.network && !f.volume
	var exts []extension
	if f.identity {
		warn(errw, "Extensions list not supported by Identity API")
	}
	for _, s := range []struct {
		want   bool
		client clientFunc
		failed string
	}{
		{f.compute, svc.compute, "Extensions list not supported by Compute API"},
		{f.volume, svc.volume, "Extensions list not supported by Block Storage API"},
		{f.network, svc.network, "Failed to retrieve extensions list from Network API"},
	} {
		if !all && !s.want {
			continue
		}
		got, err := listExtensions(ctx, s.client)
		if err != nil {
			warn(errw, fmt.Sprintf("%s: %v", s.failed, err))
			continue
		}
		exts = append(exts, got...)
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(exts))}
	for _, e := range exts {
		row := []any{e.Name, e.Alias, e.Description}
		if f.long {
			row = append(row, nilIfEmpty(e.Namespace), nilIfEmpty(e.Updated), e.Links)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func warn(w io.Writer, msg string) { _, _ = fmt.Fprintln(w, "warning: "+msg) }

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func listExtensions(ctx context.Context, client clientFunc) ([]extension, error) {
	sc, err := client()
	if err != nil {
		return nil, err
	}
	var body struct {
		Extensions []extension `json:"extensions"`
	}
	if err := extensionsGet(ctx, sc, sc.ServiceURL("extensions"), &body); err != nil {
		return nil, err
	}
	return body.Extensions, nil
}

func extensionsGet(ctx context.Context, sc *gophercloud.ServiceClient, url string, out any) error {
	resp, err := sc.Get(ctx, url, out, &gophercloud.RequestOpts{OkCodes: []int{http.StatusOK}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

func newExtensionShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <extension>",
		Short: "Show a network API extension (name or alias)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			sc, err := a.NewServiceClient(ctx, (*auth.Client).Network)
			if err != nil {
				return err
			}
			return runExtensionShow(ctx, sc, o, args[0], cmd.OutOrStdout())
		},
	}
}

// runExtensionShow finds a neutron extension by alias, then by name, as
// openstacksdk's find_extension does, and renders upstream's fields.
func runExtensionShow(ctx context.Context, sc *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	e, err := findNetworkExtension(ctx, sc, ref)
	if err != nil {
		return err
	}
	return o.WriteSingle(w,
		[]string{"alias", "description", "name", "updated_at"},
		[]any{e.Alias, e.Description, e.Name, nilIfEmpty(e.Updated)})
}

func findNetworkExtension(ctx context.Context, sc *gophercloud.ServiceClient, ref string) (*extension, error) {
	var one struct {
		Extension extension `json:"extension"`
	}
	err := extensionsGet(ctx, sc, sc.ServiceURL("extensions", ref), &one)
	if err == nil {
		return &one.Extension, nil
	}
	if !gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
		return nil, fmt.Errorf("showing extension %q: %w", ref, err)
	}
	all, lerr := listExtensions(ctx, func() (*gophercloud.ServiceClient, error) { return sc, nil })
	if lerr != nil {
		return nil, fmt.Errorf("showing extension %q: %w", ref, lerr)
	}
	var found []extension
	for _, e := range all {
		if e.Name == ref {
			found = append(found, e)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no extension found for %q", ref)
	case 1:
		return &found[0], nil
	}
	return nil, fmt.Errorf("more than one extension is named %q; use its alias", ref)
}
