package server

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newQuotaCommand builds the "quota" command group. Only the compute quotas are
// implemented here (nova quotasets); block-storage/network quotas live with
// their own services.
func newQuotaCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quota",
		Short: "Quota commands (compute only in this build)",
	}
	cmd.AddCommand(newQuotaShowCommand(a, o))
	return cmd
}

func newQuotaShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var useDefault bool
	cmd := &cobra.Command{
		Use:   "show [<project>]",
		Short: "Show compute quotas for a project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			project := ""
			if len(args) == 1 {
				project = args[0]
			} else if a.ProjectID != "" {
				project = a.ProjectID
			}
			if project == "" {
				return fmt.Errorf("no project given: pass a project ID or set OS_PROJECT_ID")
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runQuotaShow(ctx, client, o, project, useDefault, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&useDefault, "default", false, "show the default quotas instead of the project's")
	return cmd
}

func runQuotaShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, project string, useDefault bool, w io.Writer) error {
	var qs *quotasets.QuotaSet
	if useDefault {
		// gophercloud has no GetDefaults helper for compute quotasets, so the
		// os-quota-sets/{project}/defaults endpoint is fetched via the raw client.
		var body struct {
			QuotaSet quotasets.QuotaSet `json:"quota_set"`
		}
		url := client.ServiceURL("os-quota-sets", project, "defaults")
		resp, err := client.Get(ctx, url, &body, nil)
		if resp != nil {
			defer func() { _ = resp.Body.Close() }()
		}
		if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
			return fmt.Errorf("showing default compute quotas for project %q: %w", project, err)
		}
		qs = &body.QuotaSet
	} else {
		var err error
		qs, err = quotasets.Get(ctx, client, project).Extract()
		if err != nil {
			return fmt.Errorf("showing compute quotas for project %q: %w", project, err)
		}
	}

	fields := []string{
		"Instances", "Cores", "RAM", "Key Pairs", "Metadata Items",
		"Server Groups", "Server Group Members", "Injected Files",
		"Injected File Content Bytes", "Injected File Path Bytes",
	}
	values := []any{
		qs.Instances, qs.Cores, qs.RAM, qs.KeyPairs, qs.MetadataItems,
		qs.ServerGroups, qs.ServerGroupMembers, qs.InjectedFiles,
		qs.InjectedFileContentBytes, qs.InjectedFilePathBytes,
	}
	return o.WriteSingle(w, fields, values)
}
