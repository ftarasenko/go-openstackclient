package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// gophercloud v2.15.0 decodes JSON numbers into `any` with UseNumber(), so every
// number that reaches koc through a map[string]any is a json.Number rather than
// a float64. These tests pin the handling directly instead of through the SDK,
// so they hold whichever decoder the vendored version happens to use.

func TestPowerStateLabel_NumberKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want any
	}{
		{"json.Number (gophercloud >= 2.15.0)", json.Number("1"), "Running"},
		{"float64 (gophercloud < 2.15.0)", float64(1), "Running"},
		{"int", 4, "Shutdown"},
		{"json.Number, shutdown", json.Number("4"), "Shutdown"},
		{"json.Number, unmapped code passes through", json.Number("9"), json.Number("9")},
		{"json.Number, not an integer passes through", json.Number("1.5"), json.Number("1.5")},
		{"unrelated type passes through", "Running", "Running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := powerStateLabel(tc.in); got != tc.want {
				t.Errorf("powerStateLabel(%#v) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestScalarString_JSONNumber(t *testing.T) {
	// A json.Number is already the literal the server sent, so it renders
	// without the trailing ".0" a float64 round-trip would risk.
	for _, tc := range []struct{ in, want string }{
		{"1", "1"}, {"0", "0"}, {"2048", "2048"}, {"1.5", "1.5"},
	} {
		if got := scalarString(json.Number(tc.in)); got != tc.want {
			t.Errorf("scalarString(json.Number(%q)) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// ... and matches what the float64 path produces for the same value.
	if got, want := scalarString(json.Number("1")), scalarString(float64(1)); got != want {
		t.Errorf("json.Number and float64 disagree: %q vs %q", got, want)
	}
}

// TestRunServerShow_PowerStateFromDecodedBody is the end-to-end guard: the body
// is decoded the way the SDK decodes it, so this fails if a future vendor bump
// changes the number kind again and the humanizer is not taught about it.
func TestRunServerShow_PowerStateFromDecodedBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/servers/"+serverUUID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":{"id":"` + serverUUID +
			`","name":"web-1","status":"ACTIVE","OS-EXT-STS:power_state":1}}`))
	})

	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatTable}
	if err := runServerShow(t.Context(), computeClient(fakeServer, "2.93"), o, serverUUID, false, &buf); err != nil {
		t.Fatalf("runServerShow: %v", err)
	}
	if !strings.Contains(buf.String(), "Running") {
		t.Errorf("power_state was not humanized; table:\n%s", buf.String())
	}
}
