package identity

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/catalog"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names follow upstream OSC (`openstack catalog ...`). UNVERIFIED against
// KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.

func newCatalogCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "catalog", Short: "View the service catalog"}
	cmd.AddCommand(newCatalogListCommand(a, o))
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
		eps := make([]string, 0, len(e.Endpoints))
		for _, ep := range e.Endpoints {
			eps = append(eps, fmt.Sprintf("%s: %s (%s)", ep.Interface, ep.URL, ep.Region))
		}
		t.Rows = append(t.Rows, []any{e.Name, e.Type, strings.Join(eps, "\n")})
	}
	return o.WriteList(w, t)
}
