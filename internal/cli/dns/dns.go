// Package dns implements the "koc dns" command surface (zone and recordset),
// mirroring the upstream "openstack zone" / "openstack recordset" (designate v2)
// noun-verb commands.
package dns

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand builds the DNS (designate v2) command surface. Upstream splits the
// nouns two ways: "zone" and "recordset" carry no service prefix, while the
// service-level ones ("dns quota", "dns service", "dns pool") sit under "dns".
// "tsigkey" and "tld" are their own top-level nouns. The slice lets the caller
// attach them all as siblings, matching how OSC exposes them.
func NewCommand(a *auth.Options, o *output.Options) []*cobra.Command {
	return []*cobra.Command{
		newZoneCommand(a, o),
		newRecordSetCommand(a, o),
		newDNSNounCommand(a, o),
		newTSIGKeyCommand(a, o),
		newTLDCommand(a, o),
		newPTRRecordCommand(a, o),
	}
}
