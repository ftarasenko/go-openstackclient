package vaultcli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// An unknown -c fails kv copy before either Vault sees a request, so a typo
// never leaves half a tree copied behind an error.
func TestKVCopy_UnknownColumnSendsNothing(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	t.Cleanup(srv.Close)
	src, dst := clients(t, srv.URL, srv.URL)
	hits = 0

	err := runKVCopy(context.Background(), src, dst,
		&output.Options{Format: output.FormatValue, Columns: []string{"bogus"}}, copyOpts(true), io.Discard)
	var ce *output.ColumnError
	if !errors.As(err, &ce) || ce.Rendering {
		t.Fatalf("err = %v, want a pre-flight ColumnError", err)
	}
	if hits != 0 {
		t.Errorf("%d request(s) sent despite the bad column", hits)
	}
}
