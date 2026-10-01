package server

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
	const id = "4e8e5957-649f-477b-9e5b-f1f75b21c03c"
	for verb, run := range map[string]func(*output.Options, th.FakeServer) error{
		"aggregate create": func(o *output.Options, fs th.FakeServer) error {
			return runAggregateCreate(ctx, computeClient(fs, "latest"), o, "agg", &aggregateCreateFlags{}, io.Discard)
		},
		"aggregate add host": func(o *output.Options, fs th.FakeServer) error {
			return runAggregateAddHost(ctx, computeClient(fs, "latest"), o, "agg", "cmp-1", io.Discard)
		},
		"aggregate remove host": func(o *output.Options, fs th.FakeServer) error {
			return runAggregateRemoveHost(ctx, computeClient(fs, "latest"), o, "agg", "cmp-1", io.Discard)
		},
		"server group create": func(o *output.Options, fs th.FakeServer) error {
			return runServerGroupCreate(ctx, computeClient(fs, "latest"), o, "g", "affinity", nil, io.Discard)
		},
		"server add port": func(o *output.Options, fs th.FakeServer) error {
			return runServerAddPort(ctx, &computeSession{client: computeClient(fs, "latest")}, o, "web", "p", "", io.Discard)
		},
		"server add network": func(o *output.Options, fs th.FakeServer) error {
			return runServerAddNetwork(ctx, &computeSession{client: computeClient(fs, "latest")}, o, "web", "net", &attachFlags{}, io.Discard)
		},
		"server add fixed ip": func(o *output.Options, fs th.FakeServer) error {
			return runServerAddNetworkForID(ctx, computeClient(fs, "latest"), o, id, id, &attachFlags{}, io.Discard)
		},
		"server rebuild": func(o *output.Options, fs th.FakeServer) error {
			return runServerRebuild(ctx, computeClient(fs, "latest"), o, "web", &serverRebuildFlags{}, io.Discard)
		},
		"server rescue": func(o *output.Options, fs th.FakeServer) error {
			return runServerRescue(ctx, computeClient(fs, "latest"), nil, o, "web", &rescueFlags{}, io.Discard)
		},
		"server image create": func(o *output.Options, fs th.FakeServer) error {
			return runServerImageCreate(ctx, computeClient(fs, "latest"), nil, o, "web", &serverImageCreateFlags{}, io.Discard)
		},
		"console url show": func(o *output.Options, fs th.FakeServer) error {
			return runConsoleURLShow(ctx, computeClient(fs, "latest"), o, "web", "novnc", io.Discard)
		},
		"compute host drain": func(o *output.Options, fs th.FakeServer) error {
			return runHostDrain(ctx, computeClient(fs, "latest"), o, "cmp-1", drainFlags(),
				liveDrainMode(map[string]any{}), drainOutput{table: io.Discard, progress: io.Discard})
		},
	} {
		fs, calls := preflightMock(t)
		assertPreflight(t, verb, run(badColumn(), fs), *calls)
	}
}
