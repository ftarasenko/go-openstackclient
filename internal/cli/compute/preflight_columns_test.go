package compute

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
	for verb, run := range map[string]func(*output.Options, th.FakeServer) error{
		"keypair create": func(o *output.Options, fs th.FakeServer) error {
			return runKeypairCreate(ctx, computeClient(fs, "latest"), o, "k", &keypairCreateFlags{}, io.Discard)
		},
		"flavor create": func(o *output.Options, fs th.FakeServer) error {
			return runFlavorCreate(ctx, computeClient(fs, "latest"), o, "f", &flavorCreateFlags{ram: 1, vcpus: 1}, "", io.Discard)
		},
	} {
		fs, calls := preflightMock(t)
		assertPreflight(t, verb, run(badColumn(), fs), *calls)
	}
}
