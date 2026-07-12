package keyvrm

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand builds the "koc keyvrm" command tree for the KeyVRM service. KeyVRM
// nouns are namespaced under a "keyvrm" parent (rather than flat top-level nouns)
// because names like "az" and "event" would otherwise collide with generic
// OpenStack nouns, and to signal that this is the in-house KeyVRM service.
func NewCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keyvrm",
		Short: "KeyVRM (Keystack Virtual Resource Manager) commands",
		Long:  "Commands for KeyVRM, the in-house service registered in the Keystone catalog as service type \"keyvrm\".",
	}
	cmd.AddCommand(
		newAppConfigCommand(a, o),
		newHostAggregateConfigCommand(a, o),
		newAvailabilityZoneCommand(a, o),
		newEventCommand(a, o),
		newRecommendationCommand(a, o),
	)
	return cmd
}
