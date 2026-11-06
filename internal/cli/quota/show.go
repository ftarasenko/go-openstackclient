package quota

import (
	"context"
	"io"
	"maps"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	volumequotas "github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/quotasets"
	computequotas "github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
	networkquotas "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/quotas"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/koc/internal/auth"
	"github.com/ftarasenko/koc/internal/output"
	computeapi "github.com/ftarasenko/koc/pkg/openstack/compute"
	networkapi "github.com/ftarasenko/koc/pkg/openstack/network"
	volumeapi "github.com/ftarasenko/koc/pkg/openstack/volume"
)

type quotaShowFlags struct {
	useDefault bool
	services   serviceSelection
}

func newQuotaShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &quotaShowFlags{}
	cmd := &cobra.Command{
		Use:   "show [<project>]",
		Short: "Show a project's compute, volume and network quotas",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			sel := f.services.resolved()
			ctx := cmd.Context()
			s, err := newSession(ctx, a)
			if err != nil {
				return err
			}
			project, err := s.resolveProject(ctx, a, args)
			if err != nil {
				return err
			}
			return runQuotaShow(ctx, s, o, project, f.useDefault, sel, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&f.useDefault, "default", false, "show the default quotas instead of the project's")
	registerServiceFlags(cmd, &f.services)
	return cmd
}

// runQuotaShow merges the selected services' quotas into one Field/Value view.
//
// Field names are the API's own keys (cores, gigabytes, floatingip, ...) rather
// than prose labels, so a "-c" column selection reads the same as the "quota
// set" flag it corresponds to and does not change meaning between services.
func runQuotaShow(ctx context.Context, s *session, o *output.Options, project string,
	useDefault bool, sel serviceSelection, w io.Writer,
) error {
	var fields []string
	var values []any

	if sel.compute {
		client, err := s.compute()
		if err != nil {
			return err
		}
		qs, err := getComputeQuota(ctx, client, project, useDefault)
		if err != nil {
			return err
		}
		f, v := computeQuotaFields(qs)
		fields, values = append(fields, f...), append(values, v...)
	}
	if sel.volume {
		client, err := s.volume()
		if err != nil {
			return err
		}
		qs, perType, err := getVolumeQuota(ctx, client, project, useDefault)
		if err != nil {
			return err
		}
		f, v := volumeQuotaFields(qs, perType)
		fields, values = append(fields, f...), append(values, v...)
	}
	if sel.network {
		client, err := s.network()
		if err != nil {
			return err
		}
		q, err := getNetworkQuota(ctx, client, project, useDefault)
		if err != nil {
			return err
		}
		f, v := networkQuotaFields(q)
		fields, values = append(fields, f...), append(values, v...)
	}
	return o.WriteSingle(w, fields, values)
}

func getComputeQuota(ctx context.Context, client *gophercloud.ServiceClient, project string, useDefault bool) (*computequotas.QuotaSet, error) {
	if useDefault {
		return computeapi.GetDefaultQuotaSet(ctx, client, project)
	}
	return computeapi.GetQuotaSet(ctx, client, project)
}

// getVolumeQuota reads a project's cinder quota set: the typed quotas, and the
// per-volume-type ones cinder adds for every volume type
// (gigabytes_<type>, volumes_<type>, snapshots_<type>).
func getVolumeQuota(ctx context.Context, client *gophercloud.ServiceClient, project string, useDefault bool) (*volumequotas.QuotaSet, []typedQuota, error) {
	get := volumeapi.GetQuotaSet
	if useDefault {
		get = volumeapi.GetDefaultQuotaSet
	}
	qs, err := get(ctx, client, project)
	if err != nil {
		return nil, nil, err
	}
	return &qs.QuotaSet, typedQuotas(qs.PerVolumeType), nil
}

// typedQuota is one of cinder's per-volume-type quotas.
type typedQuota struct {
	key   string
	value int
}

// typedQuotas sorts the per-volume-type quotas by key so the listing is
// stable. Upstream's quota show prints every key of the quota set, these
// included.
func typedQuotas(m map[string]int) []typedQuota {
	out := make([]typedQuota, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, typedQuota{key: k, value: m[k]})
	}
	return out
}

func getNetworkQuota(ctx context.Context, client *gophercloud.ServiceClient, project string, useDefault bool) (*networkquotas.Quota, error) {
	if useDefault {
		return networkapi.GetDefaultQuota(ctx, client, project)
	}
	return networkapi.GetQuota(ctx, client, project)
}

// computeQuotaFields omits injected_files, injected_file_content_bytes and
// injected_file_path_bytes: nova removed those quotas at microversion 2.57, so
// under the negotiated "latest" they are always 0.
func computeQuotaFields(qs *computequotas.QuotaSet) ([]string, []any) {
	return []string{
			"cores", "instances", "ram", "key_pairs", "metadata_items",
			"server_groups", "server_group_members",
		},
		[]any{
			qs.Cores, qs.Instances, qs.RAM, qs.KeyPairs, qs.MetadataItems,
			qs.ServerGroups, qs.ServerGroupMembers,
		}
}

// volumeQuotaFields renders cinder's quotas, the per-volume-type ones after
// the project-wide ones.
func volumeQuotaFields(qs *volumequotas.QuotaSet, perType []typedQuota) ([]string, []any) {
	fields := []string{
		"volumes", "snapshots", "gigabytes", "per_volume_gigabytes",
		"backups", "backup_gigabytes", "groups",
	}
	values := []any{
		qs.Volumes, qs.Snapshots, qs.Gigabytes, qs.PerVolumeGigabytes,
		qs.Backups, qs.BackupGigabytes, qs.Groups,
	}
	for _, q := range perType {
		fields = append(fields, q.key)
		values = append(values, q.value)
	}
	return fields, values
}

func networkQuotaFields(q *networkquotas.Quota) ([]string, []any) {
	return []string{
			"networks", "subnets", "subnetpools", "ports", "routers", "floatingips",
			"security_groups", "security_group_rules", "rbac_policies", "trunks",
		},
		[]any{
			q.Network, q.Subnet, q.SubnetPool, q.Port, q.Router, q.FloatingIP,
			q.SecurityGroup, q.SecurityGroupRule, q.RBACPolicy, q.Trunk,
		}
}
