// Package keymanager implements the barbican (key-manager v1) command trees:
// "koc secret", "koc secret consumer", "koc secret container", "koc secret
// order" and "koc acl", mirroring python-barbicanclient's OSC plugin
// (`openstack secret …`, `openstack acl …`).
//
// Command, flag and column names follow python-barbicanclient 7.6.0
// (barbicanclient/barbican_cli/v1). The KeyStack command reference at
// https://docs.keystack.ru/ was not reachable at implementation time (HTTP
// 403), so these are UNVERIFIED against KeyStack and fall back to upstream
// semantics.
//
// Three upstream habits are kept on purpose, because scripts written against
// `openstack secret …` depend on them:
//
//   - Every resource is named by its href (or a bare UUID); the UUID at the end
//     is what the request uses, and the href is echoed back in the output.
//     Barbican has no name→ID lookup and names are not unique.
//   - The list verbs fetch a single page, --limit (default 10) entries from
//     --offset, rather than every page.
//   - Verbs are "get", not "show".
//
// Upstream's `ca get` / `ca list` are not implemented: barbican removed the
// certificate-authority API before Zed (barbican 15.0.0 has no cas
// controller), so on every cloud koc supports they could only answer 404.
package keymanager

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand returns the top-level "secret" and "acl" groups.
func NewCommand(a *auth.Options, o *output.Options) []*cobra.Command {
	secret := &cobra.Command{
		Use:   "secret",
		Short: "Key manager (barbican) secrets, containers and orders",
	}
	secret.AddCommand(
		newSecretListCommand(a, o),
		newSecretGetCommand(a, o),
		newSecretStoreCommand(a, o),
		newSecretUpdateCommand(a),
		newSecretDeleteCommand(a),
		newConsumerCommand(a, o),
		newContainerCommand(a, o),
		newOrderCommand(a, o),
	)
	return []*cobra.Command{secret, newACLCommand(a, o)}
}
