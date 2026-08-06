package quota

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	volumequotas "github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/quotasets"
	computequotas "github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
	networkquotas "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/quotas"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
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
			if f.useDefault && sel.network && !f.services.network {
				// Neutron has no quota-defaults endpoint, so a bare
				// "--default" quietly means compute+volume only. Say so rather
				// than printing a view that silently omits network rows.
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
					"note: neutron exposes no quota defaults; --default covers compute and volume only")
				sel.network = false
			}
			if f.useDefault && f.services.network {
				return fmt.Errorf("--default is not available for network quotas: neutron has no quota-defaults endpoint")
			}
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
		qs, err := getVolumeQuota(ctx, client, project, useDefault)
		if err != nil {
			return err
		}
		f, v := volumeQuotaFields(qs)
		fields, values = append(fields, f...), append(values, v...)
	}
	if sel.network {
		client, err := s.network()
		if err != nil {
			return err
		}
		q, err := networkquotas.Get(ctx, client, project).Extract()
		if err != nil {
			return fmt.Errorf("showing network quotas for project %q: %w", project, err)
		}
		f, v := networkQuotaFields(q)
		fields, values = append(fields, f...), append(values, v...)
	}
	return o.WriteSingle(w, fields, values)
}

func getComputeQuota(ctx context.Context, client *gophercloud.ServiceClient, project string, useDefault bool) (*computequotas.QuotaSet, error) {
	if !useDefault {
		qs, err := computequotas.Get(ctx, client, project).Extract()
		if err != nil {
			return nil, fmt.Errorf("showing compute quotas for project %q: %w", project, err)
		}
		return qs, nil
	}
	// gophercloud has no GetDefaults for compute quotasets, so the
	// os-quota-sets/{project}/defaults endpoint is fetched raw. Isolated here per
	// the AGENTS.md raw-fallback rule; delete once gophercloud grows the call.
	var body struct {
		QuotaSet computequotas.QuotaSet `json:"quota_set"`
	}
	resp, err := client.Get(ctx, client.ServiceURL("os-quota-sets", project, "defaults"), &body, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return nil, fmt.Errorf("showing default compute quotas for project %q: %w", project, err)
	}
	return &body.QuotaSet, nil
}

func getVolumeQuota(ctx context.Context, client *gophercloud.ServiceClient, project string, useDefault bool) (*volumequotas.QuotaSet, error) {
	get := volumequotas.Get
	what := "volume quotas"
	if useDefault {
		get = volumequotas.GetDefaults
		what = "default volume quotas"
	}
	qs, err := get(ctx, client, project).Extract()
	if err != nil {
		return nil, fmt.Errorf("showing %s for project %q: %w", what, project, err)
	}
	return qs, nil
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

func volumeQuotaFields(qs *volumequotas.QuotaSet) ([]string, []any) {
	return []string{
			"volumes", "snapshots", "gigabytes", "per_volume_gigabytes",
			"backups", "backup_gigabytes", "groups",
		},
		[]any{
			qs.Volumes, qs.Snapshots, qs.Gigabytes, qs.PerVolumeGigabytes,
			qs.Backups, qs.BackupGigabytes, qs.Groups,
		}
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
