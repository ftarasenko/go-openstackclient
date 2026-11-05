package keymanager

import (
	"context"
	"io"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	kmapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/keymanager"
)

func newConsumerCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consumer",
		Short: "Manage the consumers of a secret",
	}
	cmd.AddCommand(
		newConsumerWriteCommand(a, "create", "Register a consumer of a secret", http.MethodPost),
		newConsumerWriteCommand(a, "delete", "Remove a consumer from a secret", http.MethodDelete),
		newConsumerListCommand(a, o),
	)
	return cmd
}

// consumerFlags are the three required flags of `secret consumer create` and
// `delete`, named as python-barbicanclient's CreateConsumer/DeleteConsumer
// name them; UNVERIFIED against KeyStack.
type consumerFlags struct {
	service, resourceType, resourceID string
}

func newConsumerWriteCommand(a *auth.Options, use, short, method string) *cobra.Command {
	f := &consumerFlags{}
	cmd := &cobra.Command{
		Use:   use + " <uri>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runConsumerWrite(ctx, client, method, args[0], f)
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.service, "service-type-name", "s", "", "the service that consumes the secret")
	fl.StringVarP(&f.resourceType, "resource-type", "t", "", "the type of resource that consumes the secret")
	fl.StringVarP(&f.resourceID, "resource-id", "i", "", "the ID of the resource that consumes the secret")
	for _, name := range []string{"service-type-name", "resource-type", "resource-id"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func runConsumerWrite(ctx context.Context, client *gophercloud.ServiceClient, method, ref string, f *consumerFlags) error {
	id, err := kmapi.RefUUID(ref, "Secret")
	if err != nil {
		return err
	}
	consumer := kmapi.SecretConsumer{Service: f.service, ResourceType: f.resourceType, ResourceID: f.resourceID}
	if method == http.MethodPost {
		return kmapi.AddSecretConsumer(ctx, client, id, consumer)
	}
	return kmapi.RemoveSecretConsumer(ctx, client, id, consumer)
}

func newConsumerListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var limit, offset int
	cmd := &cobra.Command{
		Use:   "list <uri>",
		Short: "List the consumers of a secret",
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
			return runConsumerList(ctx, client, o, args[0], limit, offset, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.IntVarP(&limit, "limit", "l", 10, "the number of consumers to list (maximum: 100)")
	fl.IntVarP(&offset, "offset", "o", 0, "the page offset")
	return cmd
}

func runConsumerList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, limit, offset int, w io.Writer) error {
	id, err := kmapi.RefUUID(ref, "Secret")
	if err != nil {
		return err
	}
	consumers, err := kmapi.ListSecretConsumers(ctx, client, id, limit, offset)
	if err != nil {
		return err
	}
	t := output.Table{Columns: []string{"Service", "Resource type", "Resource id", "Created"}}
	for _, c := range consumers {
		t.Rows = append(t.Rows, []any{c.Service, c.ResourceType, c.ResourceID, c.Created})
	}
	return o.WriteList(w, t)
}
