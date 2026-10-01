package keyvrm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// An unknown -c fails the set verbs before KeyVRM sees the PUT.
func TestSetVerbs_UnknownColumnSendsNothing(t *testing.T) {
	ctx := context.Background()
	body := map[string]any{"enabled": true}
	for name, run := range map[string]func(*output.Options, th.FakeServer) error{
		"app-config set": func(o *output.Options, s th.FakeServer) error {
			return runAppConfigSet(ctx, keyvrmTestClient(s), o, body, io.Discard)
		},
		"host-aggregate-config set": func(o *output.Options, s th.FakeServer) error {
			return runHASet(ctx, keyvrmTestClient(s), o, "ha-1", body, io.Discard)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			var hits int
			fakeServer.Mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { hits++ })

			err := run(&output.Options{Format: output.FormatTable, Columns: []string{"bogus"}}, fakeServer)
			var ce *output.ColumnError
			if !errors.As(err, &ce) || ce.Rendering {
				t.Fatalf("err = %v, want a pre-flight ColumnError", err)
			}
			if hits != 0 {
				t.Errorf("%d request(s) sent despite the bad column", hits)
			}
		})
	}
}
