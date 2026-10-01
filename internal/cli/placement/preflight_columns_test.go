package placement

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

func TestWriteVerbsCheckColumnsFirst(t *testing.T) {
	ctx := context.Background()
	const rp = "4e8e5957-649f-477b-9e5b-f1f75b21c03c"
	for verb, run := range map[string]func(*output.Options, th.FakeServer) error{
		"resource provider create": func(o *output.Options, fs th.FakeServer) error {
			return runProviderCreate(ctx, placementClient(fs, "1.39"), o, "rp", "", "", io.Discard)
		},
		"resource provider set": func(o *output.Options, fs th.FakeServer) error {
			return runProviderSet(ctx, placementClient(fs, "1.39"), o, rp, &providerSetFlags{name: "x", nameSet: true}, io.Discard)
		},
		"resource provider inventory class set": func(o *output.Options, fs th.FakeServer) error {
			return runProviderInventoryClassSet(ctx, placementClient(fs, "1.39"), o, rp, "VCPU", []string{"total=8"}, io.Discard)
		},
		"resource provider inventory set": func(o *output.Options, fs th.FakeServer) error {
			return runProviderInventorySet(ctx, placementClient(fs, "1.39"), o, rp, []string{"VCPU=8"}, io.Discard)
		},
		"resource provider trait set": func(o *output.Options, fs th.FakeServer) error {
			return runProviderTraitSet(ctx, placementClient(fs, "1.39"), o, rp, []string{"CUSTOM_X"}, io.Discard)
		},
		"resource provider aggregate set": func(o *output.Options, fs th.FakeServer) error {
			return runProviderAggregateSet(ctx, placementClient(fs, "1.39"), o, rp, nil, io.Discard)
		},
		"resource provider allocation set": func(o *output.Options, fs th.FakeServer) error {
			return runProviderAllocationSet(ctx, placementClient(fs, "1.39"), o, rp,
				&allocationSetFlags{specs: []string{"rp=" + rp + ",VCPU=1"}, consumerType: "INSTANCE"}, io.Discard)
		},
		"resource provider allocation unset": func(o *output.Options, fs th.FakeServer) error {
			return runProviderAllocationUnset(ctx, placementClient(fs, "1.39"), o, rp, []string{rp}, nil, io.Discard)
		},
		"resource class set": func(o *output.Options, fs th.FakeServer) error {
			return runResourceClassSet(ctx, placementClient(fs, "1.39"), o, "CUSTOM_X", io.Discard)
		},
	} {
		fs, calls := preflightMock(t)
		assertPreflight(t, verb, run(badColumn(), fs), *calls)
	}
}
