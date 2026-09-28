package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	volumequotas "github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/quotasets"
	computequotas "github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
	networkquotas "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/quotas"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/extract"
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
	if !useDefault {
		qs, err := extract.One(computequotas.Get(ctx, client, project).Extract())
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

// getVolumeQuota reads a project's cinder quota set: the typed quotas, and the
// per-volume-type ones cinder adds for every volume type
// (gigabytes_<type>, volumes_<type>, snapshots_<type>), which QuotaSet does
// not model.
func getVolumeQuota(ctx context.Context, client *gophercloud.ServiceClient, project string, useDefault bool) (*volumequotas.QuotaSet, []typedQuota, error) {
	get := volumequotas.Get
	what := "volume quotas"
	if useDefault {
		get = volumequotas.GetDefaults
		what = "default volume quotas"
	}
	qs, perType, err := extractVolumeQuota(get(ctx, client, project))
	if err != nil {
		return nil, nil, fmt.Errorf("showing %s for project %q: %w", what, project, err)
	}
	return qs, perType, nil
}

// volumeQuotaResult is what quotasets.Get, GetDefaults and Update return.
type volumeQuotaResult interface {
	Extract() (*volumequotas.QuotaSet, error)
	ExtractInto(to any) error
}

// extractVolumeQuota decodes a cinder quota set twice: into QuotaSet, and raw
// for the per-volume-type quotas QuotaSet has no fields for.
func extractVolumeQuota(res volumeQuotaResult) (*volumequotas.QuotaSet, []typedQuota, error) {
	qs, err := extract.One(res.Extract())
	if err != nil {
		return nil, nil, err
	}
	var raw struct {
		QuotaSet map[string]json.RawMessage `json:"quota_set"`
	}
	if err := res.ExtractInto(&raw); err != nil {
		return nil, nil, err
	}
	return qs, perVolumeTypeQuotas(raw.QuotaSet), nil
}

// typedQuota is one of cinder's per-volume-type quotas.
type typedQuota struct {
	key   string
	value int
}

// perVolumeTypePrefixes are the resources cinder keeps a quota for per volume
// type, as <resource>_<type name> (cinder/quota.py VolumeTypeQuotaEngine).
var perVolumeTypePrefixes = []string{"gigabytes_", "snapshots_", "volumes_"}

// perVolumeTypeQuotas picks the per-volume-type quotas out of a raw quota set,
// sorted by key so the listing is stable. Upstream's quota show prints every
// key of the quota set, these included; a value that is not an integer is not
// a quota and is skipped.
func perVolumeTypeQuotas(set map[string]json.RawMessage) []typedQuota {
	var out []typedQuota
	for k, v := range set {
		if !hasAnyPrefix(k, perVolumeTypePrefixes) {
			continue
		}
		var n int
		if err := json.Unmarshal(v, &n); err != nil {
			continue
		}
		out = append(out, typedQuota{key: k, value: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) && len(s) > len(p) {
			return true
		}
	}
	return false
}

func getNetworkQuota(ctx context.Context, client *gophercloud.ServiceClient, project string, useDefault bool) (*networkquotas.Quota, error) {
	if !useDefault {
		q, err := extract.One(networkquotas.Get(ctx, client, project).Extract())
		if err != nil {
			return nil, fmt.Errorf("showing network quotas for project %q: %w", project, err)
		}
		return q, nil
	}
	// gophercloud has no call for neutron's quotas/{project}/default either, so
	// it is fetched raw, like the compute defaults above.
	var body struct {
		Quota networkquotas.Quota `json:"quota"`
	}
	resp, err := client.Get(ctx, client.ServiceURL("quotas", project, "default"), &body, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return nil, fmt.Errorf("showing default network quotas for project %q: %w", project, err)
	}
	return &body.Quota, nil
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
