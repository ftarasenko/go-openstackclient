package network

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Without the segments service plugin neutron has no /segments tree, and its
// bare 404 reads like a missing network. The error names the extension; a 404
// on a cloud that has it is a genuinely missing segment and is left alone.
func TestRunSegment_404NamesMissingExtension(t *testing.T) {
	for _, tc := range []struct {
		name    string
		exts    string
		explain bool
	}{
		{"plugin absent", `{"extensions": [{"alias": "router"}]}`, true},
		{"plugin loaded", `{"extensions": [{"alias": "segment"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			handleNetworkLookup(fakeServer, "public", extNetworkID)
			fakeServer.Mux.HandleFunc("/segments", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			})
			fakeServer.Mux.HandleFunc("/segments/"+extSegmentID, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			})
			fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.exts))
			})
			o := &output.Options{Format: "table"}
			for verb, err := range map[string]error{
				"list": runSegmentList(context.Background(), networkClient(fakeServer), o,
					segmentListFilter{network: "public"}, &bytes.Buffer{}),
				"show": runSegmentShow(context.Background(), networkClient(fakeServer), o, extSegmentID, &bytes.Buffer{}),
			} {
				if err == nil {
					t.Fatalf("%s: expected an error", verb)
				}
				if got := strings.Contains(err.Error(), "does not enable the segment extension"); got != tc.explain {
					t.Errorf("%s: explained = %v, want %v: %v", verb, got, tc.explain, err)
				}
			}
		})
	}
}
