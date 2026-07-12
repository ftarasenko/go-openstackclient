package server

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
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
			// The reference may be a project name or ID. Nova's quotasets
			// endpoint keys on the project ID and quietly returns the DEFAULT
			// quotas for an unknown string, so an unresolved name would silently
			// report defaults. Resolve to an ID via keystone first.
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			} else if a.ProjectID != "" {
				ref = a.ProjectID
			} else {
				ref = a.ProjectName
			}
			if ref == "" {
				return fmt.Errorf("no project given: pass a project name/ID or set OS_PROJECT_ID/OS_PROJECT_NAME")
			}
			ctx := cmd.Context()
			// newComputeSession also yields the auth bundle so the identity
			// client is only derived when a non-UUID name must be resolved.
			client, session, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			project := ref
			if !isUUID(ref) {
				identity, err := session.Identity()
				if err != nil {
					return err
				}
				project, err = resolve.ProjectID(ctx, identity, ref)
				if err != nil {
					return err
				}
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

	// The injected_files, injected_file_content_bytes and injected_file_path_bytes
	// quotas were removed from nova at microversion 2.57 and are always 0 under
	// the negotiated "latest" microversion, so they are omitted.
	fields := []string{
		"Instances", "Cores", "RAM", "Key Pairs", "Metadata Items",
		"Server Groups", "Server Group Members",
	}
	values := []any{
		qs.Instances, qs.Cores, qs.RAM, qs.KeyPairs, qs.MetadataItems,
		qs.ServerGroups, qs.ServerGroupMembers,
	}
	return o.WriteSingle(w, fields, values)
}
