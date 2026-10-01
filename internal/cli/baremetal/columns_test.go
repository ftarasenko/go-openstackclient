package baremetal

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
// cannot leave behind a node the error claims was not made.
func TestWriteVerbs_UnknownColumnSendsNothing(t *testing.T) {
	ctx := context.Background()
	for name, run := range map[string]func(*gophercloud.ServiceClient, *output.Options) error{
		"node create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runNodeCreate(ctx, c, o, &nodeCreateFlags{driver: "ipmi"}, io.Discard)
		},
		"node set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runNodeSet(ctx, c, o, "n", &nodeSetFlags{name: "x"}, io.Discard)
		},
		"node unset": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runNodeUnset(ctx, c, o, "n", &nodeUnsetFlags{name: true}, io.Discard)
		},
		"port create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runPortCreate(ctx, c, o, &portCreateFlags{}, io.Discard)
		},
		"port set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runPortSet(ctx, c, o, "p", &portSetFlags{address: "52:54:00:00:00:01"}, io.Discard)
		},
		"allocation create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runAllocationCreate(ctx, c, o, &allocationCreateFlags{}, io.Discard)
		},
		"allocation set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runAllocationSet(ctx, c, o, "a", "x", nil, io.Discard)
		},
		"allocation unset": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runAllocationUnset(ctx, c, o, "a", true, nil, io.Discard)
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
			err := run(baremetalClient(fakeServer, "1.82"), &output.Options{Format: output.FormatValue, Columns: []string{"uuid", "bogus"}})
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
