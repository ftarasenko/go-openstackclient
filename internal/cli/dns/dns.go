// Package dns implements the "koc dns" command surface (zone and recordset),
// mirroring the upstream "openstack zone" / "openstack recordset" (designate v2)
// noun-verb commands.
package dns

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand builds the DNS (designate v2) command surface: the top-level
// "zone" and "recordset" command trees. It returns a slice so the caller can
// attach both directly, matching how OSC exposes them as sibling nouns.
func NewCommand(a *auth.Options, o *output.Options) []*cobra.Command {
	return []*cobra.Command{
		newZoneCommand(a, o),
		newRecordSetCommand(a, o),
	}
}
