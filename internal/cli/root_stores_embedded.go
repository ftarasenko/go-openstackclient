//go:build koc_embedded

package cli

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// addStoreCommands registers nothing in a koc_embedded build: koc s3 and koc
// vault are not compiled in.
func addStoreCommands(*cobra.Command, *auth.Options, *output.Options) {}
