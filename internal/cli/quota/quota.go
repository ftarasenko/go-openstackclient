// Package quota implements the "koc quota" command group. Unlike the other
// packages under internal/cli it is not one service: a project's quotas live in
// nova, cinder and neutron, and upstream's `openstack quota show` / `quota set`
// present all three as one resource. It therefore derives up to three service
// clients from a single authenticated session and only talks to the ones a given
// invocation actually needs.
//
// Flag names follow upstream OSC (`openstack quota show|set`). The KeyStack
// command reference at https://docs.keystack.ru/ was not reachable at
// implementation time (HTTP 403), so these are UNVERIFIED against KeyStack and
// fall back to upstream OSC semantics.
package quota

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// NewCommand builds the "quota" command group.
func NewCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quota",
		Short: "Show and set per-project compute, volume and network quotas",
	}
	cmd.AddCommand(
		newQuotaShowCommand(a, o),
		newQuotaSetCommand(a, o),
	)
	return cmd
}

// session holds one client factory per service this command group can reach.
// They are functions rather than resolved clients so each catalog lookup happens
// only if that service's quotas are actually involved — and so tests can drive
// the run seams against a mock endpoint with no auth.
type session struct {
	compute  func() (*gophercloud.ServiceClient, error)
	volume   func() (*gophercloud.ServiceClient, error)
	network  func() (*gophercloud.ServiceClient, error)
	identity func() (*gophercloud.ServiceClient, error)
}

func newSession(ctx context.Context, a *auth.Options) (*session, error) {
	client, err := a.Authenticate(ctx)
	if err != nil {
		return nil, err
	}
	return &session{
		compute:  client.Compute,
		volume:   client.Volume,
		network:  client.Network,
		identity: client.Identity,
	}, nil
}

// resolveProject turns the optional positional project reference into a project
// ID. All three quota APIs key on the project ID and — nova most notably —
// quietly answer with the *default* quotas for an unrecognised string, so a name
// that is not resolved would silently report the wrong numbers. When no
// reference is given the invocation's own project is used.
func (s *session) resolveProject(ctx context.Context, a *auth.Options, args []string) (string, error) {
	ref := ""
	switch {
	case len(args) == 1:
		ref = args[0]
	case a.ProjectID != "":
		ref = a.ProjectID
	default:
		ref = a.ProjectName
	}
	if ref == "" {
		return "", fmt.Errorf("no project given: pass a project name/ID or set OS_PROJECT_ID/OS_PROJECT_NAME")
	}
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	identity, err := s.identity()
	if err != nil {
		return "", err
	}
	return resolve.ProjectID(ctx, identity, ref)
}

// serviceSelection records which of the three quota services an invocation
// covers. When no --compute/--volume/--network flag is given, all three are
// selected, matching upstream's merged view.
type serviceSelection struct {
	compute bool
	volume  bool
	network bool
}

func (s serviceSelection) resolved() serviceSelection {
	if !s.compute && !s.volume && !s.network {
		return serviceSelection{compute: true, volume: true, network: true}
	}
	return s
}

func registerServiceFlags(cmd *cobra.Command, s *serviceSelection) {
	fl := cmd.Flags()
	fl.BoolVar(&s.compute, "compute", false, "restrict to compute (nova) quotas")
	fl.BoolVar(&s.volume, "volume", false, "restrict to volume (cinder) quotas")
	fl.BoolVar(&s.network, "network", false, "restrict to network (neutron) quotas")
}
