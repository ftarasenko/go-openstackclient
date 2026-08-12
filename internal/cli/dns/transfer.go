package dns

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/transfer/accept"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/transfer/request"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newZoneTransferCommand builds "zone transfer ...", designate's two-step handover
// of a zone between projects: the owner creates a transfer *request* and shares
// the key it returns, then the target project *accepts* it with that key.
//
// Command names follow upstream python-designateclient
// (`openstack zone transfer request|accept …`). The KeyStack command reference at
// https://docs.keystack.ru/ was not reachable at implementation time (HTTP 403),
// so these are UNVERIFIED against KeyStack and fall back to upstream semantics.
func newZoneTransferCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "transfer",
		Short: "Transfer a zone to another project",
	}
	cmd.AddCommand(
		newZoneTransferRequestCommand(a, o),
		newZoneTransferAcceptCommand(a, o),
	)
	return cmd
}

func transferRequestFields(r *request.TransferRequest) ([]string, []any) {
	fields := []string{
		"id", "zone_id", "zone_name", "project_id", "target_project_id",
		"key", "description", "status", "created_at", "updated_at",
	}
	values := []any{
		r.ID, r.ZoneID, r.ZoneName, r.ProjectID, r.TargetProjectID,
		r.Key, r.Description, r.Status, dnsTime(r.CreatedAt), dnsTime(r.UpdatedAt),
	}
	return fields, values
}

func transferAcceptFields(ac *accept.TransferAccept) ([]string, []any) {
	fields := []string{
		"id", "status", "project_id", "zone_id", "key",
		"zone_transfer_request_id", "created_at", "updated_at",
	}
	values := []any{
		ac.ID, ac.Status, ac.ProjectID, ac.ZoneID, ac.Key,
		ac.ZoneTransferRequestID, dnsTime(ac.CreatedAt), dnsTime(ac.UpdatedAt),
	}
	return fields, values
}

// --- zone transfer request -------------------------------------------------

func newZoneTransferRequestCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "request",
		Short: "Manage zone transfer requests (the owner's half of a handover)",
	}
	cmd.AddCommand(
		newZoneTransferRequestCreateCommand(a, o),
		newZoneTransferRequestListCommand(a, o),
		newZoneTransferRequestShowCommand(a, o),
		newZoneTransferRequestSetCommand(a, o),
		newZoneTransferRequestDeleteCommand(a, o),
	)
	return cmd
}

func newZoneTransferRequestCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var targetProject, targetProjectDomain, description string
	cmd := &cobra.Command{
		Use:   "create <zone>",
		Short: "Offer a zone for transfer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newDNSSession(ctx, a)
			if err != nil {
				return err
			}
			// An omitted --target-project makes the offer open to any project that
			// has the key, which is designate's own default.
			targetID := ""
			if targetProject != "" {
				targetID, err = resolveTargetProjectID(ctx, session, targetProject, targetProjectDomain)
				if err != nil {
					return err
				}
			}
			return runZoneTransferRequestCreate(ctx, client, o, args[0], targetID, description, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&targetProject, "target-project", "",
		"restrict the transfer to this project (name or ID); omit to let any project with the key accept")
	fl.StringVar(&targetProjectDomain, "target-project-domain", "",
		"domain owning the target project, to disambiguate the name (name or ID)")
	fl.StringVar(&description, "description", "", "description for the transfer request")
	return cmd
}

func runZoneTransferRequestCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	zoneRef, targetProjectID, description string, w io.Writer,
) error {
	zoneID, err := resolveZoneID(ctx, client, zoneRef)
	if err != nil {
		return err
	}
	tr, err := request.Create(ctx, client, zoneID, request.CreateOpts{
		TargetProjectID: targetProjectID,
		Description:     description,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating transfer request for zone %q: %w", zoneRef, err)
	}
	fields, values := transferRequestFields(tr)
	return o.WriteSingle(w, fields, values)
}

func newZoneTransferRequestListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List zone transfer requests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneTransferRequestList(ctx, client, o, status, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by status, e.g. ACTIVE or COMPLETE")
	return cmd
}

func runZoneTransferRequestList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	status string, w io.Writer,
) error {
	pages, err := request.List(client, request.ListOpts{Status: status}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing zone transfer requests: %w", err)
	}
	all, err := request.ExtractTransferRequests(pages)
	if err != nil {
		return fmt.Errorf("parsing zone transfer request list: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Zone ID", "Zone Name", "Target Project ID", "Status", "Description"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, tr := range all {
		t.Rows = append(t.Rows, []any{tr.ID, tr.ZoneID, tr.ZoneName, tr.TargetProjectID, tr.Status, tr.Description})
	}
	return o.WriteList(w, t)
}

func newZoneTransferRequestShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <transfer-request-id>",
		Short: "Show a zone transfer request",
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
			return runZoneTransferRequestShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

// Transfer requests have no name, so the reference is always the ID.
func runZoneTransferRequestShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	tr, err := request.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing zone transfer request %s: %w", id, err)
	}
	fields, values := transferRequestFields(tr)
	return o.WriteSingle(w, fields, values)
}

func newZoneTransferRequestSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var targetProject, targetProjectDomain, description string
	cmd := &cobra.Command{
		Use:   "set <transfer-request-id>",
		Short: "Update a zone transfer request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			fl := cmd.Flags()
			if !fl.Changed("target-project") && !fl.Changed("description") {
				return fmt.Errorf("nothing to set: pass --target-project and/or --description")
			}
			ctx := cmd.Context()
			client, session, err := newDNSSession(ctx, a)
			if err != nil {
				return err
			}
			targetID := ""
			if targetProject != "" {
				targetID, err = resolveTargetProjectID(ctx, session, targetProject, targetProjectDomain)
				if err != nil {
					return err
				}
			}
			return runZoneTransferRequestSet(ctx, client, o, args[0], targetID, description, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&targetProject, "target-project", "", "restrict the transfer to this project (name or ID)")
	fl.StringVar(&targetProjectDomain, "target-project-domain", "", "domain owning the target project (name or ID)")
	fl.StringVar(&description, "description", "", "new description")
	return cmd
}

// runZoneTransferRequestSet sends both fields; UpdateOpts tags each omitempty, so
// an unset one is simply absent from the body.
func runZoneTransferRequestSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id, targetProjectID, description string, w io.Writer,
) error {
	tr, err := request.Update(ctx, client, id, request.UpdateOpts{
		TargetProjectID: targetProjectID,
		Description:     description,
	}).Extract()
	if err != nil {
		return fmt.Errorf("updating zone transfer request %s: %w", id, err)
	}
	fields, values := transferRequestFields(tr)
	return o.WriteSingle(w, fields, values)
}

func newZoneTransferRequestDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <transfer-request-id> [<transfer-request-id>...]",
		Short: "Withdraw one or more zone transfer requests",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneTransferRequestDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runZoneTransferRequestDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string, w io.Writer) error {
	return batchdelete.Each(ids, func(id string) error {
		if err := request.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting zone transfer request %s: %w", id, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted zone transfer request %s\n", id); err != nil {
			return err
		}
		return nil
	})
}

// --- zone transfer accept --------------------------------------------------

// newZoneTransferAcceptCommand has no delete or set verb: designate's accept
// resource supports only Create/List/Get, since accepting a transfer is a
// one-shot action rather than an editable record.
func newZoneTransferAcceptCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "accept",
		Short: "Accept a zone transfer (the target project's half of a handover)",
	}
	cmd.AddCommand(
		newZoneTransferAcceptRequestCommand(a, o),
		newZoneTransferAcceptListCommand(a, o),
		newZoneTransferAcceptShowCommand(a, o),
	)
	return cmd
}

// newZoneTransferAcceptRequestCommand is spelled "accept request" to match
// upstream (`openstack zone transfer accept request`), which reads as "accept the
// request" rather than naming a noun.
func newZoneTransferAcceptRequestCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var transferID, key string
	cmd := &cobra.Command{
		Use:   "request",
		Short: "Accept a zone transfer request using its key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if transferID == "" || key == "" {
				return fmt.Errorf("--transfer-id and --key are both required: the key is what authorises the transfer")
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneTransferAcceptRequest(ctx, client, o, transferID, key, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&transferID, "transfer-id", "", "ID of the zone transfer request to accept (required)")
	fl.StringVar(&key, "key", "", "transfer key the zone's owner shared with you (required)")
	return cmd
}

func runZoneTransferAcceptRequest(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	transferID, key string, w io.Writer,
) error {
	ac, err := accept.Create(ctx, client, accept.CreateOpts{
		ZoneTransferRequestID: transferID,
		Key:                   key,
	}).Extract()
	if err != nil {
		return fmt.Errorf("accepting zone transfer request %s: %w", transferID, err)
	}
	fields, values := transferAcceptFields(ac)
	return o.WriteSingle(w, fields, values)
}

func newZoneTransferAcceptListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List zone transfer accepts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newDNSClient(ctx, a)
			if err != nil {
				return err
			}
			return runZoneTransferAcceptList(ctx, client, o, status, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by status, e.g. COMPLETE")
	return cmd
}

func runZoneTransferAcceptList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	status string, w io.Writer,
) error {
	pages, err := accept.List(client, accept.ListOpts{Status: status}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing zone transfer accepts: %w", err)
	}
	all, err := accept.ExtractTransferAccepts(pages)
	if err != nil {
		return fmt.Errorf("parsing zone transfer accept list: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Zone ID", "Project ID", "Zone Transfer Request ID", "Status"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, ac := range all {
		t.Rows = append(t.Rows, []any{ac.ID, ac.ZoneID, ac.ProjectID, ac.ZoneTransferRequestID, ac.Status})
	}
	return o.WriteList(w, t)
}

func newZoneTransferAcceptShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <transfer-accept-id>",
		Short: "Show a zone transfer accept",
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
			return runZoneTransferAcceptShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runZoneTransferAcceptShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	ac, err := accept.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing zone transfer accept %s: %w", id, err)
	}
	fields, values := transferAcceptFields(ac)
	return o.WriteSingle(w, fields, values)
}
