package crossservice

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/usage"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// `usage list` and `usage show` read nova's os-simple-tenant-usage, which
// reports per-project resource-hours over a time window.
//
// Flag names follow upstream OSC (`openstack usage list|show`). UNVERIFIED
// against KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at
// implementation time); falls back to upstream OSC semantics.

// usageDateLayout is the date form upstream documents ("ex 2012-01-20"). A
// full RFC 3339 timestamp is accepted too, for a window narrower than a day.
const usageDateLayout = "2006-01-02"

type usageFlags struct {
	start string
	end   string
}

func (f *usageFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.start, "start", "", "usage range start date, e.g. 2026-01-20 (default: four weeks ago)")
	fl.StringVar(&f.end, "end", "", "usage range end date, e.g. 2026-02-20 (default: tomorrow)")
}

// window resolves the flags into the pair nova expects. The defaults mirror
// upstream: four weeks back to tomorrow, so "today" is fully included.
func (f *usageFlags) window(now time.Time) (start, end time.Time, err error) {
	start = now.AddDate(0, 0, -28)
	end = now.AddDate(0, 0, 1)
	if f.start != "" {
		if start, err = parseUsageDate(f.start); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("--start: %w", err)
		}
	}
	if f.end != "" {
		if end, err = parseUsageDate(f.end); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("--end: %w", err)
		}
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("--end %s is before --start %s",
			end.Format(usageDateLayout), start.Format(usageDateLayout))
	}
	return start, end, nil
}

func parseUsageDate(s string) (time.Time, error) {
	if t, err := time.Parse(usageDateLayout, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected a date like 2026-01-20 or an RFC 3339 timestamp, got %q", s)
	}
	return t, nil
}

// --- usage list -------------------------------------------------------------

func newUsageListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &usageFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List compute resource usage for every project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := a.Authenticate(ctx)
			if err != nil {
				return err
			}
			compute, err := client.Compute()
			if err != nil {
				return err
			}
			return runUsageList(ctx, compute, o, f, time.Now(), cmd.OutOrStdout())
		},
	}
	f.register(cmd)
	return cmd
}

func runUsageList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *usageFlags, now time.Time, w io.Writer,
) error {
	start, end, err := f.window(now)
	if err != nil {
		return err
	}
	// Detailed is what makes nova include server_usages; without it the summary
	// response carries only the totals and the Servers column below counts an
	// empty slice, reporting 0 for every project no matter how many servers ran.
	// Upstream OSC passes detailed=True for the same reason.
	pages, err := usage.AllTenants(client, usage.AllTenantsOpts{
		Start:    &start,
		End:      &end,
		Detailed: true,
	}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing compute usage: %w", err)
	}
	all, err := usage.ExtractAllTenants(pages)
	if err != nil {
		return fmt.Errorf("parsing the compute usage list: %w", err)
	}
	t := output.Table{
		Columns: []string{"Project ID", "Servers", "RAM MB-Hours", "CPU Hours", "Disk GB-Hours"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, u := range all {
		t.Rows = append(t.Rows, []any{
			u.TenantID, len(u.ServerUsages), u.TotalMemoryMBUsage, u.TotalVCPUsUsage, u.TotalLocalGBUsage,
		})
	}
	return o.WriteList(w, t)
}

// --- usage show -------------------------------------------------------------

func newUsageShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &usageFlags{}
	var project string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show compute resource usage for one project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := a.Authenticate(ctx)
			if err != nil {
				return err
			}
			projectID, err := resolveUsageProject(ctx, client, project)
			if err != nil {
				return err
			}
			compute, err := client.Compute()
			if err != nil {
				return err
			}
			return runUsageShow(ctx, compute, o, projectID, f, time.Now(), cmd.OutOrStdout())
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&project, "project", "", "project to show usage for (name or ID; default: the scoped project)")
	return cmd
}

// resolveUsageProject falls back to the project the token is scoped to, which
// is what upstream does when --project is omitted. The scoped project is not
// carried on the client, so it comes from introspecting the current token —
// the same route "token issue" takes.
func resolveUsageProject(ctx context.Context, client *auth.Client, ref string) (string, error) {
	identity, err := client.Identity()
	if err != nil {
		return "", err
	}
	if ref != "" {
		return resolve.ProjectID(ctx, identity, ref)
	}
	project, err := tokens.Get(ctx, identity, client.Provider.Token()).ExtractProject()
	if err != nil {
		return "", fmt.Errorf("introspecting the current token to find the scoped project: %w", err)
	}
	if project == nil || project.ID == "" {
		return "", fmt.Errorf("no --project given and the token is not scoped to a project")
	}
	return project.ID, nil
}

func runUsageShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	projectID string, f *usageFlags, now time.Time, w io.Writer,
) error {
	start, end, err := f.window(now)
	if err != nil {
		return err
	}
	// EachPage, not AllPages: the single-tenant body is one object rather than a
	// list, and gophercloud's AllPages merges pages by their top-level array —
	// so it hands ExtractSingleTenant a body with no `tenant_usage` key and the
	// extract returns (nil, nil). gophercloud's own tests use EachPage for
	// exactly this call.
	//
	// From microversion 2.40 nova paginates server_usages, so the server count
	// is accumulated across pages while the totals (which cover the whole
	// window) are taken from the first.
	var u *usage.TenantUsage
	servers := 0
	err = usage.SingleTenant(client, projectID, usage.SingleTenantOpts{Start: &start, End: &end}).
		EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
			got, perr := usage.ExtractSingleTenant(page)
			if perr != nil {
				return false, perr
			}
			if got == nil {
				return true, nil
			}
			if u == nil {
				u = got
			}
			servers += len(got.ServerUsages)
			return true, nil
		})
	if err != nil {
		return fmt.Errorf("getting compute usage for project %s: %w", projectID, err)
	}
	if u == nil {
		return fmt.Errorf("nova reported no usage for project %s between %s and %s",
			projectID, start.Format(usageDateLayout), end.Format(usageDateLayout))
	}
	return o.WriteSingle(w,
		[]string{"project_id", "servers", "ram_mb_hours", "cpu_hours", "disk_gb_hours", "start", "stop"},
		[]any{u.TenantID, servers, u.TotalMemoryMBUsage, u.TotalVCPUsUsage, u.TotalLocalGBUsage,
			u.Start.Format(time.RFC3339), u.Stop.Format(time.RFC3339)})
}
