package baremetal

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/conductors"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newConductorCommand builds "baremetal conductor ...".
func newConductorCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "conductor",
		Short: "Manage baremetal conductors",
	}
	cmd.AddCommand(newConductorListCommand(a, o))
	return cmd
}

// conductorListFlags holds the filters accepted by "conductor list".
//
// Flag names follow upstream OSC (`openstack baremetal conductor list`). The
// KeyStack command reference at https://docs.keystack.ru/ was not reachable at
// implementation time (HTTP 403), so these are UNVERIFIED against KeyStack and
// fall back to upstream OSC semantics.
type conductorListFlags struct {
	long    bool
	limit   int
	marker  string
	sortKey string
	sortDir string
}

func newConductorListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &conductorListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List baremetal conductors",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runConductorList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of conductors to return")
	fl.StringVar(&f.marker, "marker", "", "hostname of the last conductor from the previous page")
	fl.StringVar(&f.sortKey, "sort-key", "", "sort output by this conductor attribute")
	fl.StringVar(&f.sortDir, "sort-dir", "", "sort direction: asc or desc")
	return cmd
}

func runConductorList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *conductorListFlags, w io.Writer) error {
	opts := conductors.ListOpts{
		Limit:   f.limit,
		Marker:  f.marker,
		SortKey: f.sortKey,
		SortDir: f.sortDir,
	}
	if f.long {
		opts.Detail = true
	}
	pages, err := conductors.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing baremetal conductors: %w", err)
	}
	all, err := conductors.ExtractConductors(pages)
	if err != nil {
		return fmt.Errorf("parsing baremetal conductor list: %w", err)
	}
	all = capResults(all, f.limit)
	return o.WriteList(w, conductorListTable(all, f.long))
}

func conductorListTable(list []conductors.Conductor, long bool) output.Table {
	cols := []string{"Hostname", "Conductor Group", "Alive"}
	if long {
		cols = append(cols, "Drivers", "Updated At")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for _, c := range list {
		row := []any{c.Hostname, c.ConductorGroup, c.Alive}
		if long {
			row = append(row, c.Drivers, c.UpdatedAt)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}
