package compute

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const keypairListBody = `{
  "keypairs": [
    {"keypair": {"name": "key-a", "fingerprint": "aa:bb:cc", "type": "ssh", "public_key": "ssh-rsa AAAA"}},
    {"keypair": {"name": "key-b", "fingerprint": "dd:ee:ff", "type": "ssh", "public_key": "ssh-rsa BBBB"}}
  ]
}`

func TestRunKeypairList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotAPIVersion string
	fakeServer.Mux.HandleFunc("/os-keypairs", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(keypairListBody))
	})

	client := computeClient(fakeServer, "2.2")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runKeypairList(context.Background(), client, o, &buf); err != nil {
		t.Fatalf("runKeypairList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotAPIVersion != "compute 2.2" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.2")
	}

	out := buf.String()
	for _, want := range []string{
		"Name", "Fingerprint", "Type",
		"key-a", "key-b", "aa:bb:cc", "dd:ee:ff", "ssh",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunKeypairList_ValueFormatIsTabSeparatedNoHeader(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-keypairs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(keypairListBody))
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatValue}

	var buf bytes.Buffer
	if err := runKeypairList(context.Background(), client, o, &buf); err != nil {
		t.Fatalf("runKeypairList returned error: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "Fingerprint") {
		t.Errorf("value format must not include headers:\n%s", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("value format: got %d rows, want 2:\n%s", len(lines), out)
	}
	first := strings.Split(lines[0], "\t")
	if first[0] != "key-a" || first[1] != "aa:bb:cc" {
		t.Errorf("unexpected value row fields: %#v", first)
	}
}
