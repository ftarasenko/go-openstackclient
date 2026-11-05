package identity

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/catalog"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
)

// ListCatalog lists the service catalog of the token the client holds.
func ListCatalog(ctx context.Context, c *gophercloud.ServiceClient) ([]tokens.CatalogEntry, error) {
	return listAll(ctx, catalog.List(c), catalog.ExtractServiceCatalog, "listing catalog")
}

// TokenInfo is what introspecting a token returns. Project and User are nil
// when the token carries none (a domain- or system-scoped token has no
// project). Token.ID is read from the X-Subject-Token response header and can
// be empty; the caller already has the token.
type TokenInfo struct {
	Token   *tokens.Token
	Project *tokens.Project
	User    *tokens.User
}

// IntrospectToken validates token and reads its expiry, project and user.
func IntrospectToken(ctx context.Context, c *gophercloud.ServiceClient, token string) (*TokenInfo, error) {
	res := tokens.Get(ctx, c, token)
	tok, err := res.ExtractToken()
	if err != nil {
		return nil, wrap(err, "introspecting token")
	}
	info := &TokenInfo{Token: tok}
	if p, perr := res.ExtractProject(); perr == nil {
		info.Project = p
	}
	if u, uerr := res.ExtractUser(); uerr == nil {
		info.User = u
	}
	return info, nil
}

// TokenUser returns the user token was issued to.
func TokenUser(ctx context.Context, c *gophercloud.ServiceClient, token string) (*tokens.User, error) {
	u, err := tokens.Get(ctx, c, token).ExtractUser()
	return u, wrap(err, "introspecting token")
}

// RevokeToken revokes token. Keystone takes it in a header, so the client
// may authenticate as anyone allowed to revoke it.
func RevokeToken(ctx context.Context, c *gophercloud.ServiceClient, token string) error {
	return wrap(tokens.Revoke(ctx, c, token).Err, "revoking token")
}
