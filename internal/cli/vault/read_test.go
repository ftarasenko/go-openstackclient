package vaultcli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/vault"
)

func readFixture(t *testing.T) *vault.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/v1/kv/metadata/dev?list=true":
			_, _ = w.Write([]byte(`{"data":{"keys":["openrc","nested/"]}}`))
		case "/v1/kv/data/dev/openrc?":
			_, _ = w.Write([]byte(`{"data":{"data":{"OS_USERNAME":"admin","OS_PASSWORD":"p"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	c, err := vault.New(context.Background(), vault.Config{Addr: srv.URL, Token: "t", KVMount: "kv"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRunKVList(t *testing.T) {
	c := readFixture(t)
	var buf bytes.Buffer
	if err := runKVList(context.Background(), c, &output.Options{Format: output.FormatValue}, "dev", &buf); err != nil {
		t.Fatal(err)
	}
	// Sorted, folders keeping Vault's trailing slash.
	if got := buf.String(); got != "nested/\nopenrc\n" {
		t.Errorf("output = %q", got)
	}

	err := runKVList(context.Background(), c, &output.Options{Format: output.FormatValue}, "missing", &buf)
	if err == nil || !strings.Contains(err.Error(), "no keys under") {
		t.Errorf("err = %v, want a friendly not-found message", err)
	}
}

func TestRunKVGet(t *testing.T) {
	c := readFixture(t)
	var buf bytes.Buffer
	if err := runKVGet(context.Background(), c, &output.Options{Format: output.FormatJSON}, "dev/openrc", 0, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"OS_PASSWORD", "OS_USERNAME", "admin"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
