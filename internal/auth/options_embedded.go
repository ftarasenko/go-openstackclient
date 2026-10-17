//go:build koc_embedded

package auth

import "github.com/spf13/pflag"

// addCredentialSourceFlags registers nothing in a koc_embedded build: the
// --creds-from-*, Kubernetes and Vault code is not compiled in.
func (o *Options) addCredentialSourceFlags(*pflag.FlagSet) {}
