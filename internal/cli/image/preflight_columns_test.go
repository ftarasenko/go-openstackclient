package image

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
	const img = "4e8e5957-649f-477b-9e5b-f1f75b21c03c"
	for verb, run := range map[string]func(*output.Options, th.FakeServer) error{
		"image create": func(o *output.Options, fs th.FakeServer) error {
			return runImageCreate(ctx, imageClient(fs), o, "i", &imageCreateFlags{}, nil, io.Discard)
		},
		"image create --volume": func(o *output.Options, fs th.FakeServer) error {
			return runImageCreateFromVolume(ctx, volumeClient(fs, "3.70"), o, "i", img, &imageCreateFlags{}, io.Discard)
		},
		"image set": func(o *output.Options, fs th.FakeServer) error {
			return runImageSet(ctx, imageClient(fs), o, img, &imageSetFlags{name: "x"}, io.Discard)
		},
		"image unset": func(o *output.Options, fs th.FakeServer) error {
			return runImageUnset(ctx, imageClient(fs), o, img, &imageUnsetFlags{property: []string{"k"}}, io.Discard)
		},
		"image add project": func(o *output.Options, fs th.FakeServer) error {
			return runImageAddProject(ctx, imageClient(fs), o, img, "p1", io.Discard)
		},
		"image member set": func(o *output.Options, fs th.FakeServer) error {
			return runImageMemberSet(ctx, imageClient(fs), o, img, "p1", "accepted", io.Discard)
		},
	} {
		fs, calls := preflightMock(t)
		assertPreflight(t, verb, run(badColumn(), fs), *calls)
	}
}
