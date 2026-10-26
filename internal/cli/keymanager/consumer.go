package keymanager

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Secret consumers are a raw fallback: gophercloud v2.15.0's keymanager has
// consumers for containers only, not for secrets. They are barbican's
// microversion 1.1 API, which every release from Zed carries; the client's
// pinned microversion already sends it.
//
//	POST|DELETE /v1/secrets/{id}/consumers   {"service", "resource_type", "resource_id"}
//	GET         /v1/secrets/{id}/consumers?limit=&offset=

// secretConsumer is one entry of GET /v1/secrets/{id}/consumers.
type secretConsumer struct {
	Service      string `json:"service"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Created      string `json:"created"`
}

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
	id, err := refUUID(ref, "Secret")
	if err != nil {
		return err
	}
	body := map[string]string{"service": f.service, "resource_type": f.resourceType, "resource_id": f.resourceID}
	u := client.ServiceURL("secrets", id, "consumers")
	var resp *http.Response
	if method == http.MethodPost {
		resp, err = client.Post(ctx, u, body, nil, &gophercloud.RequestOpts{OkCodes: []int{http.StatusOK, http.StatusCreated}})
	} else {
		resp, err = client.Delete(ctx, u, &gophercloud.RequestOpts{JSONBody: body, OkCodes: []int{http.StatusOK, http.StatusNoContent}})
	}
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		verb := "registering consumer of"
		if method == http.MethodDelete {
			verb = "removing consumer from"
		}
		return fmt.Errorf("%s secret %s: %w", verb, id, err)
	}
	return nil
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
	id, err := refUUID(ref, "Secret")
	if err != nil {
		return err
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}}
	var body struct {
		Consumers []secretConsumer `json:"consumers"`
	}
	resp, err := client.Get(ctx, client.ServiceURL("secrets", id, "consumers")+"?"+q.Encode(), &body, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return fmt.Errorf("listing consumers of secret %s: %w", id, err)
	}
	t := output.Table{Columns: []string{"Service", "Resource type", "Resource id", "Created"}}
	for _, c := range body.Consumers {
		t.Rows = append(t.Rows, []any{c.Service, c.ResourceType, c.ResourceID, c.Created})
	}
	return o.WriteList(w, t)
}
