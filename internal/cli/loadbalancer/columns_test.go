package loadbalancer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// An unknown -c fails every write verb before it sends anything, so a typo
// cannot leave behind a resource the error claims was not made.
func TestWriteVerbs_UnknownColumnSendsNothing(t *testing.T) {
	ctx := context.Background()
	for name, run := range map[string]func(*gophercloud.ServiceClient, *output.Options) error{
		"loadbalancer create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runLBCreate(ctx, c, o, "lb", &lbCreateFlags{}, resolvedLBRefs{}, io.Discard)
		},
		"loadbalancer set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runLBSet(ctx, c, o, "lb", &lbSetFlags{}, changedSet{"name": true}, io.Discard)
		},
		"listener create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runListenerCreate(ctx, c, o, "l", &listenerWriteFlags{}, "", io.Discard)
		},
		"listener set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runListenerSet(ctx, c, o, "l", &listenerWriteFlags{}, changedSet{"name": true}, io.Discard)
		},
		"pool create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runPoolCreate(ctx, c, o, "p", &poolWriteFlags{}, "", io.Discard)
		},
		"pool set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runPoolSet(ctx, c, o, "p", &poolWriteFlags{}, changedSet{"name": true}, io.Discard)
		},
		"member create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runMemberCreate(ctx, c, o, "p", "m", &memberWriteFlags{}, io.Discard)
		},
		"member set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runMemberSet(ctx, c, o, "p", "m", &memberWriteFlags{}, io.Discard)
		},
		"healthmonitor create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runHealthMonitorCreate(ctx, c, o, "p", "hm", &healthMonitorWriteFlags{}, io.Discard)
		},
		"healthmonitor set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runHealthMonitorSet(ctx, c, o, "hm", &healthMonitorWriteFlags{}, changedSet{"name": true}, io.Discard)
		},
		"l7policy create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runL7PolicyCreate(ctx, c, o, "pol", &l7PolicyWriteFlags{}, "", io.Discard)
		},
		"l7policy set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runL7PolicySet(ctx, c, o, "pol", &l7PolicyWriteFlags{}, changedSet{"name": true}, io.Discard)
		},
		"l7rule create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runL7RuleCreate(ctx, c, o, "pol", &l7RuleWriteFlags{}, "", io.Discard)
		},
		"l7rule set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runL7RuleSet(ctx, c, o, "pol", "rule", &l7RuleWriteFlags{}, io.Discard)
		},
		"flavor create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runFlavorCreate(ctx, c, o, "f", &octaviaFlavorCreateFlags{}, io.Discard)
		},
		"flavor set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runFlavorSet(ctx, c, o, "f", &octaviaFlavorSetFlags{changed: changedSet{"name": true}}, io.Discard)
		},
		"flavorprofile create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runFlavorProfileCreate(ctx, c, o, "fp", "amphora", "{}", io.Discard)
		},
		"flavorprofile set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runFlavorProfileSet(ctx, c, o, "fp", &flavorProfileSetFlags{}, io.Discard)
		},
		"quota set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runLBQuotaSet(ctx, c, o, "proj", &lbQuotaSetFlags{}, changedSet{"pool": true}, io.Discard)
		},
		"quota unset": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runLBQuotaUnset(ctx, c, o, "proj", []string{"pool"}, io.Discard)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			var calls int
			fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(http.StatusInternalServerError)
			})
			err := run(lbClient(fakeServer), &output.Options{Format: output.FormatValue, Columns: []string{"id", "bogus"}})
			var ce *output.ColumnError
			if !errors.As(err, &ce) || ce.Rendering {
				t.Fatalf("err = %v, want a pre-flight ColumnError", err)
			}
			if calls != 0 {
				t.Errorf("%d request(s) sent despite the bad column, want none", calls)
			}
		})
	}
}
