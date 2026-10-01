package identity

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// preflightMock counts every request and answers none, so a test can assert
// that a rejected -c sent nothing.
func preflightMock(t *testing.T) (th.FakeServer, *int) {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	var calls int
	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	})
	return fakeServer, &calls
}

// badColumn is a -c selection no command renders.
func badColumn() *output.Options {
	return &output.Options{Format: output.FormatValue, Columns: []string{"bogus"}}
}

// assertPreflight checks that err is a -c rejection raised before any request.
func assertPreflight(t *testing.T, verb string, err error, calls int) {
	t.Helper()
	var ce *output.ColumnError
	if !errors.As(err, &ce) || ce.Rendering {
		t.Errorf("%s: err = %v, want a pre-flight *output.ColumnError", verb, err)
	}
	if calls != 0 {
		t.Errorf("%s: %d request(s) sent despite the bad column, want none", verb, calls)
	}
}

// Every identity write verb that renders a result rejects an unknown -c before
// it sends anything, name lookups included.
func TestWriteVerbsCheckColumnsFirst(t *testing.T) {
	ctx := context.Background()
	for verb, run := range map[string]func(*output.Options, th.FakeServer) error{
		"application credential create": func(o *output.Options, fs th.FakeServer) error {
			return runAppCredCreate(ctx, identityClient(fs), o, "u1", "ac", &appCredCreateFlags{roles: []string{"member"}}, io.Discard)
		},
		"domain create": func(o *output.Options, fs th.FakeServer) error {
			return runDomainCreate(ctx, identityClient(fs), o, "d", &domainWriteFlags{}, io.Discard)
		},
		"endpoint create": func(o *output.Options, fs th.FakeServer) error {
			return runEndpointCreate(ctx, identityClient(fs), o,
				endpointRef{service: "nova", iface: "public", url: "https://example.com"}, &endpointWriteFlags{}, io.Discard)
		},
		"project create": func(o *output.Options, fs th.FakeServer) error {
			return runProjectCreate(ctx, identityClient(fs), o, "p", &projectWriteFlags{domain: "dom"}, io.Discard)
		},
		"user create": func(o *output.Options, fs th.FakeServer) error {
			return runUserCreate(ctx, identityClient(fs), o, "u", &userWriteFlags{domain: "dom"}, io.Discard)
		},
		"service create": func(o *output.Options, fs th.FakeServer) error {
			return runServiceCreate(ctx, identityClient(fs), o, "compute", &serviceWriteFlags{}, io.Discard)
		},
		"region create": func(o *output.Options, fs th.FakeServer) error {
			return runRegionCreate(ctx, identityClient(fs), o, "r1", "", "", io.Discard)
		},
		"role create": func(o *output.Options, fs th.FakeServer) error {
			return runRoleCreate(ctx, identityClient(fs), o, "r", &roleCreateFlags{domain: "dom"}, io.Discard)
		},
		"implied role create": func(o *output.Options, fs th.FakeServer) error {
			return runImpliedRoleCreate(ctx, identityClient(fs), o, "a", "b", io.Discard)
		},
		"group create": func(o *output.Options, fs th.FakeServer) error {
			return runGroupCreate(ctx, identityClient(fs), o, "g", &groupWriteFlags{domain: "dom"}, io.Discard)
		},
		"group set": func(o *output.Options, fs th.FakeServer) error {
			return runGroupSet(ctx, identityClient(fs), o, "g", &groupWriteFlags{name: "h"}, false, io.Discard)
		},
	} {
		fs, calls := preflightMock(t)
		assertPreflight(t, verb, run(badColumn(), fs), *calls)
	}
}
