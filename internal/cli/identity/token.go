package identity

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// token issue introspects the token minted during authentication. There is no
// dedicated "issue" verb in keystone; OSC simply reports the current token. We
// take the token string from the provider (auth.Client.Provider.Token()) and
// introspect it with tokens.Get to recover the expiry, scoped project and user.
// UNVERIFIED against KeyStack docs (https://docs.keystack.ru/ returned HTTP 403
// at implementation time); falls back to upstream OSC semantics.

func newTokenCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Manage tokens"}
	cmd.AddCommand(newTokenIssueCommand(a, o))
	return cmd
}

func newTokenIssueCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "issue",
		Short: "Show the current authentication token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, ac, err := newIdentityAuthClient(ctx, a)
			if err != nil {
				return err
			}
			return runTokenIssue(ctx, client, o, ac.Provider.Token(), cmd.OutOrStdout())
		},
	}
}

func runTokenIssue(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, token string, w io.Writer) error {
	res := tokens.Get(ctx, client, token)
	tok, err := res.ExtractToken()
	if err != nil {
		return fmt.Errorf("introspecting token: %w", err)
	}
	// ID from ExtractToken is read from the X-Subject-Token response header, which
	// introspection does not echo; fall back to the token we submitted.
	id := tok.ID
	if id == "" {
		id = token
	}

	var projectID, userID string
	if p, err := res.ExtractProject(); err == nil && p != nil {
		projectID = p.ID
	}
	if u, err := res.ExtractUser(); err == nil && u != nil {
		userID = u.ID
	}

	return o.WriteSingle(w,
		[]string{"id", "expires", "project_id", "user_id"},
		[]any{id, formatTime(tok.ExpiresAt), projectID, userID})
}
