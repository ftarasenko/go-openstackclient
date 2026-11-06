package crossservice

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	volumelimits "github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/limits"
	computelimits "github.com/gophercloud/gophercloud/v2/openstack/compute/v2/limits"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	"github.com/ftarasenko/koc/internal/cli/resolve"
	"github.com/ftarasenko/koc/internal/output"
	computeapi "github.com/ftarasenko/koc/pkg/openstack/compute"
	"github.com/ftarasenko/koc/pkg/openstack/ratelimit"
	volumeapi "github.com/ftarasenko/koc/pkg/openstack/volume"
)

// `limits show` merges nova's and cinder's absolute limits into one listing.
//
// Flag names follow upstream OSC (`openstack limits show`). UNVERIFIED against
// KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.

func newLimitsShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var absolute, rate bool
	var project string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show compute and volume limits",
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
			return runLimitsShow(ctx, client, o, absolute, rate, project, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&absolute, "absolute", false, "show absolute limits")
	fl.BoolVar(&rate, "rate", false, "show rate limits (nova always reports none; cinder's are config-driven)")
	fl.StringVar(&project, "project", "", "show limits for this project (name or ID; admin only)")
	cmd.MarkFlagsMutuallyExclusive("absolute", "rate")
	cmd.MarkFlagsOneRequired("absolute", "rate")
	return cmd
}

func runLimitsShow(ctx context.Context, client *auth.Client, o *output.Options,
	absolute, rate bool, projectRef string, w io.Writer,
) error {
	projectID, err := resolveLimitsProject(ctx, client, projectRef)
	if err != nil {
		return err
	}
	if rate {
		return showRateLimits(ctx, client, o, projectID, w)
	}
	_ = absolute
	return showAbsoluteLimits(ctx, client, o, projectID, w)
}

func resolveLimitsProject(ctx context.Context, client *auth.Client, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	identity, err := client.Identity()
	if err != nil {
		return "", err
	}
	return resolve.ProjectID(ctx, identity, ref)
}

// showAbsoluteLimits renders one Name/Value row per limit, merging both
// services. Upstream prefixes nothing, and the names do not collide because
// nova's are maxTotal*/total*Used and cinder's are maxTotalVolume*/totalVolumes*.
func showAbsoluteLimits(ctx context.Context, client *auth.Client, o *output.Options, projectID string, w io.Writer) error {
	t := output.Table{Columns: []string{"Name", "Value"}}

	compute, err := client.Compute()
	if err != nil {
		return err
	}
	cl, err := computeapi.GetLimits(ctx, compute, projectID)
	if err != nil {
		return err
	}
	t.Rows = append(t.Rows, computeAbsoluteRows(cl.Absolute)...)

	volume, err := client.Volume()
	if err != nil {
		return err
	}
	vl, err := volumeapi.GetLimits(ctx, volume)
	if err != nil {
		return err
	}
	t.Rows = append(t.Rows, volumeAbsoluteRows(vl.Absolute)...)
	return o.WriteList(w, t)
}

func computeAbsoluteRows(a computelimits.Absolute) [][]any {
	return [][]any{
		{"maxTotalInstances", a.MaxTotalInstances},
		{"maxTotalCores", a.MaxTotalCores},
		{"maxTotalRAMSize", a.MaxTotalRAMSize},
		{"maxTotalKeypairs", a.MaxTotalKeypairs},
		{"maxServerGroups", a.MaxServerGroups},
		{"maxServerGroupMembers", a.MaxServerGroupMembers},
		{"maxServerMeta", a.MaxServerMeta},
		{"maxTotalFloatingIps", a.MaxTotalFloatingIps},
		{"maxSecurityGroups", a.MaxSecurityGroups},
		{"maxSecurityGroupRules", a.MaxSecurityGroupRules},
		{"totalInstancesUsed", a.TotalInstancesUsed},
		{"totalCoresUsed", a.TotalCoresUsed},
		{"totalRAMUsed", a.TotalRAMUsed},
		{"totalServerGroupsUsed", a.TotalServerGroupsUsed},
		{"totalFloatingIpsUsed", a.TotalFloatingIpsUsed},
		{"totalSecurityGroupsUsed", a.TotalSecurityGroupsUsed},
	}
}

func volumeAbsoluteRows(a volumelimits.Absolute) [][]any {
	return [][]any{
		{"maxTotalVolumes", a.MaxTotalVolumes},
		{"maxTotalVolumeGigabytes", a.MaxTotalVolumeGigabytes},
		{"maxTotalSnapshots", a.MaxTotalSnapshots},
		{"maxTotalBackups", a.MaxTotalBackups},
		{"maxTotalBackupGigabytes", a.MaxTotalBackupGigabytes},
		{"totalVolumesUsed", a.TotalVolumesUsed},
		{"totalGigabytesUsed", a.TotalGigabytesUsed},
		{"totalSnapshotsUsed", a.TotalSnapshotsUsed},
		{"totalBackupsUsed", a.TotalBackupsUsed},
		{"totalBackupGigabytesUsed", a.TotalBackupGigabytesUsed},
	}
}

// showRateLimits reads the `rate` array both services return alongside the
// absolute limits. gophercloud's Limits structs model only `absolute`, so
// ratelimit.List reads them raw from the same endpoint.
//
// Nova has not had rate limits for years — its view hardcodes `"rate": []`
// (nova/api/openstack/compute/views/limits.py) — so in practice only cinder
// can produce rows here, and only when the operator configured them. An empty
// table is the normal answer, not a failure.
func showRateLimits(ctx context.Context, client *auth.Client, o *output.Options, projectID string, w io.Writer) error {
	t := output.Table{Columns: []string{"Service", "Verb", "URI", "Regex", "Limit", "Remaining", "Unit", "Next Available"}}

	compute, err := client.Compute()
	if err != nil {
		return err
	}
	if err := appendRateLimits(ctx, compute, "compute", projectID, &t); err != nil {
		return err
	}
	volume, err := client.Volume()
	if err != nil {
		return err
	}
	if err := appendRateLimits(ctx, volume, "volume", "", &t); err != nil {
		return err
	}
	return o.WriteList(w, t)
}

func appendRateLimits(ctx context.Context, sc *gophercloud.ServiceClient, service, projectID string, t *output.Table) error {
	limits, err := ratelimit.List(ctx, sc, projectID)
	if err != nil {
		return fmt.Errorf("getting %s rate limits: %w", service, errors.Unwrap(err))
	}
	for _, l := range limits {
		t.Rows = append(t.Rows, []any{service, l.Verb, l.URI, l.Regex, l.Value, l.Remaining, l.Unit, l.NextAvailable})
	}
	return nil
}
