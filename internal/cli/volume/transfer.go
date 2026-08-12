package volume

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/transfers"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/paging"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "volume transfer request" — cinder's hand-off of a volume from one project to
// another: the owner creates a request, hands the ID and auth key to the
// recipient, and the recipient accepts it.
//
// Flag names follow upstream OSC (`openstack volume transfer request ...`).
// UNVERIFIED against KeyStack docs (https://docs.keystack.ru/ returned HTTP 403
// at implementation time); falls back to upstream OSC semantics.

// transferSnapshotsMicroversion is where cinder added the `no_snapshots` field,
// on a new /volume-transfers endpoint. The legacy /os-volume-transfer route
// gophercloud uses has no such field and always carries the snapshots, so
// --no-snapshots has to go to the newer endpoint.
const transferSnapshotsMicroversion = "3.55"

func newTransferCommand(a *auth.Options, o *output.Options) *cobra.Command {
	request := &cobra.Command{Use: "request", Short: "Manage volume transfer requests"}
	request.AddCommand(
		newTransferListCommand(a, o),
		newTransferShowCommand(a, o),
		newTransferCreateCommand(a, o),
		newTransferDeleteCommand(a, o),
		newTransferAcceptCommand(a, o),
	)
	cmd := &cobra.Command{Use: "transfer", Short: "Transfer volumes between projects"}
	cmd.AddCommand(request)
	return cmd
}

func transferShowFields(t *transfers.Transfer) ([]string, []any) {
	return []string{"id", "name", "volume_id", "auth_key", "created_at"},
		[]any{t.ID, t.Name, t.VolumeID, t.AuthKey, t.CreatedAt}
}

// --- list / show ------------------------------------------------------------

func newTransferListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var allProjects bool
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List volume transfer requests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runTransferList(ctx, client, o, allProjects, limit, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&allProjects, "all-projects", false, "list transfer requests from all projects (admin only)")
	fl.IntVar(&limit, "limit", 0, "maximum number of transfer requests to return")
	return cmd
}

func runTransferList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	allProjects bool, limit int, w io.Writer,
) error {
	opts := transfers.ListOpts{AllTenants: allProjects, Limit: limit}
	all, err := paging.Collect(ctx, transfers.List(client, opts), limit, transfers.ExtractTransfers)
	if err != nil {
		return fmt.Errorf("listing volume transfer requests: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Volume ID"}, Rows: make([][]any, 0, len(all))}
	for _, tr := range all {
		t.Rows = append(t.Rows, []any{tr.ID, tr.Name, tr.VolumeID})
	}
	return o.WriteList(w, t)
}

func newTransferShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <transfer-request>",
		Short: "Show a volume transfer request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runTransferShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runTransferShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	tr, err := transfers.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing volume transfer request %s: %w", id, err)
	}
	fields, values := transferShowFields(tr)
	return o.WriteSingle(w, fields, values)
}

// --- create -----------------------------------------------------------------

func newTransferCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var name string
	var snapshots, noSnapshots bool
	cmd := &cobra.Command{
		Use:   "create <volume>",
		Short: "Create a volume transfer request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runTransferCreate(ctx, client, o, args[0], name, noSnapshots, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "name for the transfer request")
	fl.BoolVar(&snapshots, "snapshots", false, "transfer the volume's snapshots too (cinder's default)")
	fl.BoolVar(&noSnapshots, "no-snapshots", false,
		"leave the volume's snapshots behind (requires cinder 3.55)")
	cmd.MarkFlagsMutuallyExclusive("snapshots", "no-snapshots")
	return cmd
}

// runTransferCreate creates the request and prints the auth key.
//
// The key is returned exactly once, by this call — cinder never shows it again,
// and the recipient cannot accept the transfer without it — so it goes through
// the output layer as part of the result rather than to a log line.
func runTransferCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	volumeRef, name string, noSnapshots bool, w io.Writer,
) error {
	volumeID, err := resolveVolumeID(ctx, client, volumeRef)
	if err != nil {
		return err
	}
	var tr *transfers.Transfer
	if noSnapshots {
		if tr, err = createTransferWithoutSnapshots(ctx, client, volumeID, name); err != nil {
			return fmt.Errorf("creating a transfer request for volume %q: %w", volumeRef, err)
		}
	} else {
		tr, err = transfers.Create(ctx, client, transfers.CreateOpts{VolumeID: volumeID, Name: name}).Extract()
		if err != nil {
			return fmt.Errorf("creating a transfer request for volume %q: %w", volumeRef, err)
		}
	}
	fields, values := transferShowFields(tr)
	return o.WriteSingle(w, fields, values)
}

// createTransferWithoutSnapshots POSTs to cinder's newer /volume-transfers
// endpoint, the only one that accepts `no_snapshots` (microversion 3.55).
// gophercloud v2.13.0 targets the legacy /os-volume-transfer route, which has
// no such field, so this cannot go through the typed call. Delete it once
// gophercloud grows the newer endpoint.
func createTransferWithoutSnapshots(ctx context.Context, client *gophercloud.ServiceClient,
	volumeID, name string,
) (*transfers.Transfer, error) {
	transfer := map[string]any{
		"volume_id":    volumeID,
		"no_snapshots": true,
	}
	if name != "" {
		transfer["name"] = name
	}
	body := map[string]any{"transfer": transfer}
	// Pin the request to the microversion that introduced the field: on a copy
	// of the client, since setMicroversionHeader rewrites MoreHeaders on every
	// request made with it.
	pinned := *client
	pinned.Microversion = transferSnapshotsMicroversion

	var result struct {
		Transfer transfers.Transfer `json:"transfer"`
	}
	resp, err := pinned.Post(ctx, pinned.ServiceURL("volume-transfers"), body, &result, &gophercloud.RequestOpts{
		OkCodes: []int{202},
	})
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return nil, err
	}
	return &result.Transfer, nil
}

// --- delete / accept --------------------------------------------------------

func newTransferDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <transfer-request> [<transfer-request> ...]",
		Short: "Delete volume transfer request(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runTransferDelete(ctx, client, args)
		},
	}
}

func runTransferDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string) error {
	return batchdelete.Each(ids, func(id string) error {
		if err := transfers.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting volume transfer request %s: %w", id, err)
		}
		return nil
	})
}

func newTransferAcceptCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var authKey string
	cmd := &cobra.Command{
		Use:   "accept <transfer-request>",
		Short: "Accept a volume transfer request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runTransferAccept(ctx, client, o, args[0], authKey, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&authKey, "auth-key", "", "auth key from the transfer request")
	_ = cmd.MarkFlagRequired("auth-key")
	return cmd
}

func runTransferAccept(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id, authKey string, w io.Writer,
) error {
	tr, err := transfers.Accept(ctx, client, id, transfers.AcceptOpts{AuthKey: authKey}).Extract()
	if err != nil {
		return fmt.Errorf("accepting volume transfer request %s: %w", id, err)
	}
	fields, values := transferShowFields(tr)
	return o.WriteSingle(w, fields, values)
}
