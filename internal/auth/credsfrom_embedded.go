//go:build koc_embedded

package auth

import (
	"context"
	"errors"

	"github.com/gophercloud/gophercloud/v2"
)

// A koc_embedded build compiles in none of the Kubernetes and Vault credential
// sources (credsfrom.go), and registers none of their flags, so these are only
// reachable through Options fields set programmatically. They say so.

var errNoCredSources = errors.New("--creds-from-ns and --creds-from-vault are not part of this build")

type ironicCreds struct{}

func (*ironicCreds) baremetalClient(string) (*gophercloud.ServiceClient, error) {
	return nil, errNoCredSources
}

func (o *Options) loadIronicCreds(context.Context) (*ironicCreds, error) {
	return nil, errNoCredSources
}

func (o *Options) applyVaultOpenrc(context.Context) error { return errNoCredSources }

// readVaultTokenFile reads no ~/.vault-token: there is no Vault in this build.
func readVaultTokenFile() string { return "" }
