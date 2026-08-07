package identity

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/catalog"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names follow upstream OSC (`openstack catalog ...`). UNVERIFIED against
// KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.

func newCatalogCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "catalog", Short: "View the service catalog"}
	cmd.AddCommand(
		newCatalogListCommand(a, o),
		newCatalogShowCommand(a, o),
	)
	return cmd
}

func newCatalogListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List catalog entries",
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
			return runCatalogList(ctx, client, o, cmd.OutOrStdout())
		},
	}
}

func runCatalogList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	pages, err := catalog.List(client).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing catalog: %w", err)
	}
	all, err := catalog.ExtractServiceCatalog(pages)
	if err != nil {
		return fmt.Errorf("parsing catalog: %w", err)
	}
	t := output.Table{Columns: []string{"Name", "Type", "Endpoints"}, Rows: make([][]any, 0, len(all))}
	for _, e := range all {
		t.Rows = append(t.Rows, []any{e.Name, e.Type, catalogEndpoints(e)})
	}
	return o.WriteList(w, t)
}

// newCatalogShowCommand builds "catalog show <service>", mirroring upstream
// `openstack catalog show`. The argument is the service's name or its catalog
// type ("keystone" or "identity"), as upstream accepts either.
func newCatalogShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <service>",
		Short: "Show one catalog entry by service name or type",
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
			return runCatalogShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runCatalogShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	pages, err := catalog.List(client).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing catalog: %w", err)
	}
	all, err := catalog.ExtractServiceCatalog(pages)
	if err != nil {
		return fmt.Errorf("parsing catalog: %w", err)
	}
	// Name first, then type: a deployment may name a service after another
	// service's type, and upstream resolves the name in that case.
	for _, match := range []func(tokens.CatalogEntry) bool{
		func(e tokens.CatalogEntry) bool { return e.Name == ref },
		func(e tokens.CatalogEntry) bool { return e.Type == ref },
	} {
		for _, e := range all {
			if match(e) {
				return o.WriteSingle(w,
					[]string{"id", "name", "type", "endpoints"},
					[]any{e.ID, e.Name, e.Type, catalogEndpoints(e)})
			}
		}
	}
	return fmt.Errorf("no service catalog entry named or typed %q", ref)
}

// catalogEndpoints renders an entry's endpoints one per line, as `catalog list`
// does — the table layer renders the newlines as separate physical lines.
func catalogEndpoints(e tokens.CatalogEntry) string {
	eps := make([]string, 0, len(e.Endpoints))
	for _, ep := range e.Endpoints {
		eps = append(eps, fmt.Sprintf("%s: %s (%s)", ep.Interface, ep.URL, ep.Region))
	}
	return strings.Join(eps, "\n")
}
