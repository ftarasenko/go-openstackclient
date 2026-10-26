package keymanager

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/orders"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func newOrderCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "order",
		Short: "Manage orders: secrets and key pairs barbican generates",
	}
	cmd.AddCommand(
		newOrderListCommand(a, o),
		newOrderGetCommand(a, o),
		newOrderCreateCommand(a, o),
		newOrderDeleteCommand(a),
	)
	return cmd
}

// orderColumns are upstream's KeyOrderFormatter/AsymmetricOrderFormatter
// columns; the two share them.
var orderColumns = []string{
	"Order href", "Type", "Container href", "Secret href",
	"Created", "Status", "Error code", "Error message",
}

// orderRow renders an order as upstream does: a key order has no container and
// an asymmetric one no secret, each shown as "N/A".
func orderRow(r *orders.Order) []any {
	container, secret := any(r.ContainerRef), any(r.SecretRef)
	switch r.Type {
	case "key":
		container = "N/A"
	case "asymmetric":
		secret = "N/A"
	}
	typ := r.Type
	if typ != "" {
		typ = strings.ToUpper(typ[:1]) + typ[1:]
	}
	var code, msg any
	if r.ErrorStatusCode != "" {
		code = r.ErrorStatusCode
	}
	if r.ErrorReason != "" {
		msg = r.ErrorReason
	}
	return []any{r.OrderRef, typ, container, secret, r.Created, r.Status, code, msg}
}

func newOrderListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var limit, offset int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List orders",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runOrderList(ctx, client, o, orders.ListOpts{Limit: limit, Offset: offset}, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.IntVarP(&limit, "limit", "l", 10, "the number of orders to list (maximum: 100)")
	fl.IntVarP(&offset, "offset", "o", 0, "the page offset")
	return cmd
}

func runOrderList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, opts orders.ListOpts, w io.Writer) error {
	page, err := firstPage(ctx, orders.List(client, opts))
	if err != nil {
		return fmt.Errorf("listing orders: %w", err)
	}
	t := output.Table{Columns: orderColumns}
	if page == nil {
		return o.WriteList(w, t)
	}
	all, err := orders.ExtractOrders(page)
	if err != nil {
		return fmt.Errorf("parsing order list: %w", err)
	}
	for i := range all {
		t.Rows = append(t.Rows, orderRow(&all[i]))
	}
	return o.WriteList(w, t)
}

func newOrderGetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "get <uri>",
		Short: "Retrieve an order by its URI",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runOrderGet(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runOrderGet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := refUUID(ref, "Order")
	if err != nil {
		return err
	}
	return showOrder(ctx, client, o, id, w)
}

func showOrder(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	r, err := orders.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting order %s: %w", id, err)
	}
	return o.WriteSingle(w, orderColumns, orderRow(r))
}

// orderCreateFlags holds `secret order create`'s options; names and defaults
// follow python-barbicanclient's CreateOrder. UNVERIFIED against KeyStack.
type orderCreateFlags struct {
	name, algorithm, mode          string
	payloadContentType, expiration string
	bitLength                      int
}

func newOrderCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &orderCreateFlags{}
	cmd := &cobra.Command{
		Use:       "create <key|asymmetric>",
		Short:     "Order a generated secret (key) or key pair (asymmetric)",
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs: []string{"key", "asymmetric"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			opts, err := f.createOpts(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runOrderCreate(ctx, client, o, opts, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.name, "name", "n", "", "a human-friendly name")
	fl.StringVarP(&f.algorithm, "algorithm", "a", "aes", "the algorithm of the requested key")
	fl.IntVarP(&f.bitLength, "bit-length", "b", 256, "the bit length of the requested key")
	fl.StringVarP(&f.mode, "mode", "m", "cbc", "the algorithm mode of the requested key")
	fl.StringVarP(&f.payloadContentType, flagPayloadContentType, "t", "application/octet-stream",
		"the type/format of the secret to be generated")
	fl.StringVarP(&f.expiration, flagExpiration, "x", "", "the expiration time for the secret in ISO 8601 format")
	return cmd
}

func (f *orderCreateFlags) createOpts(typ string) (orders.CreateOpts, error) {
	opts := orders.CreateOpts{
		Type: orders.OrderType(typ),
		Meta: orders.MetaOpts{
			Name: f.name, Algorithm: f.algorithm, BitLength: f.bitLength,
			Mode: f.mode, PayloadContentType: f.payloadContentType,
		},
	}
	if f.expiration != "" {
		t, err := parseExpiration(f.expiration)
		if err != nil {
			return opts, err
		}
		opts.Meta.Expiration = t
	}
	return opts, nil
}

func runOrderCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, opts orders.CreateOpts, w io.Writer) error {
	created, err := orders.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating order: %w", err)
	}
	id, err := refUUID(created.OrderRef, "Order")
	if err != nil {
		return fmt.Errorf("creating order: barbican answered order_ref %q: %w", created.OrderRef, err)
	}
	return showOrder(ctx, client, o, id, w)
}

func newOrderDeleteCommand(a *auth.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <uri>",
		Short: "Delete an order by its URI",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runOrderDelete(ctx, client, args[0])
		},
	}
}

func runOrderDelete(ctx context.Context, client *gophercloud.ServiceClient, ref string) error {
	id, err := refUUID(ref, "Order")
	if err != nil {
		return err
	}
	if err := orders.Delete(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting order %s: %w", id, err)
	}
	return nil
}
