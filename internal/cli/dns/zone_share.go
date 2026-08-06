package dns

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/zones"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newZoneShareCommand builds "zone share ...".
//
// Command and flag names follow upstream python-designateclient
// (`openstack zone share create|list|show|delete`). The KeyStack command
// reference at https://docs.keystack.ru/ was not reachable at implementation
// time (HTTP 403), so these are UNVERIFIED against KeyStack and fall back to
// upstream semantics.
func newZoneShareCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "share",
		Short: "Share a zone with another project",
	}
	cmd.AddCommand(
		newZoneShareCreateCommand(a, o),
		newZoneShareListCommand(a, o),
		newZoneShareShowCommand(a, o),
		newZoneShareDeleteCommand(a, o),
	)
	return cmd
}

func zoneShareFields(s *zones.ZoneShare) ([]string, []any) {
	return []string{"id", "zone_id", "project_id", "target_project_id", "created_at", "updated_at"},
		[]any{s.ID, s.ZoneID, s.ProjectID, s.TargetProjectID, s.CreatedAt, s.UpdatedAt}
}

// --- create ----------------------------------------------------------------

func newZoneShareCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var targetProjectDomain string
	cmd := &cobra.Command{
		Use:   "create <zone> <target-project>",
		Short: "Share a zone with a target project",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newDNSSession(ctx, a)
			if err != nil {
				return err
			}
			targetID, err := resolveTargetProjectID(ctx, session, args[1], targetProjectDomain)
			if err != nil {
				return err
			}
			return runZoneShareCreate(ctx, client, o, args[0], targetID, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&targetProjectDomain, "target-project-domain", "",
		"domain owning the target project, to disambiguate the name (name or ID)")
	return cmd
}

// resolveTargetProjectID turns the target-project reference into a keystone ID.
// Designate stores only the ID, so a name has to be resolved here; the identity
// client is derived only when the reference is not already a UUID.
func resolveTargetProjectID(ctx context.Context, session *auth.Client, ref, domainRef string) (string, error) {
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	identity, err := session.Identity()
	if err != nil {
		return "", err
	}
	return resolve.ProjectIDInDomain(ctx, identity, ref, domainRef)
}

func runZoneShareCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	zoneRef, targetProjectID string, w io.Writer,
) error {
	zoneID, err := resolveZoneID(ctx, client, zoneRef)
	if err != nil {
		return err
	}
	share, err := zones.Share(ctx, client, zoneID, zones.ShareZoneOpts{TargetProjectID: targetProjectID}).Extract()
	if err != nil {
		return fmt.Errorf("sharing zone %q with project %q: %w", zoneRef, targetProjectID, err)
	}
	fields, values := zoneShareFields(share)
	return o.WriteSingle(w, fields, values)
}

// --- list ------------------------------------------------------------------

func newZoneShareListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var allProjects bool
	cmd := &cobra.Command{
		Use:   "list <zone>",
		Short: "List the projects a zone is shared with",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneShareList(ctx, client, o, args[0], allProjects, cmd.OutOrStdout())
		},
	}
	// Designate's cross-project reads use the X-Auth-All-Projects header rather
	// than a query parameter; gophercloud's ListSharesOpts carries it.
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "list shares across all projects (admin)")
	return cmd
}

func runZoneShareList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	zoneRef string, allProjects bool, w io.Writer,
) error {
	zoneID, err := resolveZoneID(ctx, client, zoneRef)
	if err != nil {
		return err
	}
	pages, err := zones.ListShares(client, zoneID, zones.ListSharesOpts{AllProjects: allProjects}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing shares of zone %q: %w", zoneRef, err)
	}
	all, err := zones.ExtractZoneShares(pages)
	if err != nil {
		return fmt.Errorf("parsing zone share list: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Zone ID", "Target Project ID", "Created At"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, s := range all {
		t.Rows = append(t.Rows, []any{s.ID, s.ZoneID, s.TargetProjectID, s.CreatedAt})
	}
	return o.WriteList(w, t)
}

// --- show ------------------------------------------------------------------

func newZoneShareShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <zone> <share-id>",
		Short: "Show details of one zone share",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneShareShow(ctx, client, o, args[0], args[1], cmd.OutOrStdout())
		},
	}
}

func runZoneShareShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	zoneRef, shareID string, w io.Writer,
) error {
	zoneID, err := resolveZoneID(ctx, client, zoneRef)
	if err != nil {
		return err
	}
	share, err := zones.GetShare(ctx, client, zoneID, shareID).Extract()
	if err != nil {
		return fmt.Errorf("showing share %s of zone %q: %w", shareID, zoneRef, err)
	}
	fields, values := zoneShareFields(share)
	return o.WriteSingle(w, fields, values)
}

// --- delete ----------------------------------------------------------------

func newZoneShareDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <zone> <share-id> [<share-id>...]",
		Short: "Stop sharing a zone with a project",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneShareDelete(ctx, client, args[0], args[1:], cmd.OutOrStdout())
		},
	}
}

func runZoneShareDelete(ctx context.Context, client *gophercloud.ServiceClient,
	zoneRef string, shareIDs []string, w io.Writer,
) error {
	zoneID, err := resolveZoneID(ctx, client, zoneRef)
	if err != nil {
		return err
	}
	for _, shareID := range shareIDs {
		if derr := zones.Unshare(ctx, client, zoneID, shareID).ExtractErr(); derr != nil {
			return fmt.Errorf("removing share %s from zone %q: %w", shareID, zoneRef, derr)
		}
		if _, werr := fmt.Fprintf(w, "Removed share %s from zone %s\n", shareID, zoneRef); werr != nil {
			return werr
		}
	}
	return nil
}
