package dns

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	dnsapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/dns"
	osresolve "github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

// The designate calls, typed and raw, live in pkg/openstack/dns; this package
// keeps the CLI's policy on top of them: its error wording, the literal-ref
// fallback of the admin nouns' lookups, and rendering.

// relabel re-labels a pkg/ error that names an ID with the CLI's text, which
// names the operator's reference instead.
func relabel(err error, format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, errors.Unwrap(err))...)
}

// relabelList is relabel for a listing, whose parse error keeps pkg/'s
// wording.
func relabelList(err error, format string, args ...any) error {
	if strings.HasPrefix(err.Error(), "parsing ") {
		return err
	}
	return relabel(err, format, args...)
}

// lookup applies the admin nouns' lenient policy over a strict pkg/ resolver:
// a UUID passes, no match falls back to the literal ref, and several matches
// are an error worded by ambiguous (the ref and the count).
func lookup(ambiguous, ref string, find func() (string, error)) (string, error) {
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	id, err := find()
	var notFound *osresolve.NotFoundError
	var many *osresolve.AmbiguousError
	switch {
	case err == nil:
		return id, nil
	case errors.As(err, &notFound):
		return ref, nil
	case errors.As(err, &many):
		return "", fmt.Errorf(ambiguous, ref, many.Count)
	}
	return "", err
}

// commonOptions are designate's cross-cutting CLI options
// (designateclient/v2/cli/common.py). Both travel as request headers, not query
// parameters, and both need an admin role server-side.
type commonOptions struct {
	allProjects   bool
	sudoProjectID string
}

// bind registers --all-projects/--sudo-project-id on a command; upstream accepts
// them on every designate verb, write ones included.
func (c *commonOptions) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.BoolVar(&c.allProjects, "all-projects", false, "act across all projects (admin)")
	fl.StringVar(&c.sudoProjectID, "sudo-project-id", "", "project ID to impersonate for this command (admin)")
}

// scope renders the options as pkg/'s Scope; the zero Scope sends no extra
// headers at all.
func (c *commonOptions) scope() dnsapi.Scope {
	if c == nil {
		return dnsapi.Scope{}
	}
	return dnsapi.Scope{AllProjects: c.allProjects, SudoProjectID: c.sudoProjectID}
}

// headers renders the options as the headers designate reads them from, and nil
// when neither was given.
func (c *commonOptions) headers() map[string]string { return c.scope().Headers() }

// withCommonHeaders returns a shallow copy of the service client carrying the
// common headers. Copying rather than mutating matters: one invocation shares
// the client, and MoreHeaders is read on every request it makes.
func withCommonHeaders(client *gophercloud.ServiceClient, c *commonOptions) *gophercloud.ServiceClient {
	return dnsapi.WithScope(client, c.scope())
}

// client builds the designate service client for a command with the common
// headers already applied, so *every* request the command makes carries them —
// not just its final one.
//
// That distinction is the whole point. These verbs take a zone or recordset by
// name and resolve it to an ID first, and a resource in another project cannot
// be found by name unless the lookup itself is cross-project. Upstream gets this
// for free because `common.set_all_common_headers` mutates the session
// (designateclient/v2/cli/common.py), which every subsequent call inherits;
// setting them on the client is the same scope.
func (c *commonOptions) client(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, error) {
	client, err := newDNSClient(ctx, a)
	if err != nil {
		return nil, err
	}
	return withCommonHeaders(client, c), nil
}

// session is commonOptions.client for the one verb that also needs keystone
// ("zone share create" takes its target project by name).
func (c *commonOptions) session(ctx context.Context, a *auth.Options) (*gophercloud.ServiceClient, *auth.Client, error) {
	client, session, err := newDNSSession(ctx, a)
	if err != nil {
		return nil, nil, err
	}
	return withCommonHeaders(client, c), session, nil
}

// --- timestamps ------------------------------------------------------------

// dnsTimeLayout is designate's own timestamp spelling, e.g.
// "2026-08-06T12:00:00.000000". The koc-owned DTOs (zone export/import, pool,
// blacklist, TLD, service status) carry designate's string through untouched,
// so formatting the gophercloud-parsed time.Time values the same way is what
// makes one convention out of two.
const dnsTimeLayout = "2006-01-02T15:04:05.000000"

// dnsTime renders a designate timestamp that gophercloud parsed into a
// time.Time. A null timestamp — an unshared zone's transferred_at, a
// never-updated record's updated_at — arrives as the Go zero time, which
// rendered as the meaningless "0001-01-01 00:00:00 +0000 UTC". It becomes nil
// here instead, so it shows as empty in table/value/csv and as null in
// json/yaml, matching how every other absent value renders in koc. (Upstream
// prints the string "None"; nil is the same idea without lying to a JSON
// consumer.)
func dnsTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(dnsTimeLayout)
}

// dnsTimeString is the same convention for the raw-string DTOs, where designate
// signals "no timestamp" with a JSON null that decodes to "".
func dnsTimeString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
