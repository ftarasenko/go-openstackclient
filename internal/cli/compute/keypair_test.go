package compute

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

const keypairGetBody = `{
  "keypair": {
    "name": "key-a",
    "fingerprint": "aa:bb:cc",
    "type": "ssh",
    "public_key": "ssh-rsa AAAA",
    "user_id": "u-1"
  }
}`

func TestRunKeypairShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotAPIVersion, gotPath string
	fakeServer.Mux.HandleFunc("/os-keypairs/key-a", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(keypairGetBody))
	})

	client := computeClient(fakeServer, "2.2")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runKeypairShow(context.Background(), client, o, "key-a", &buf); err != nil {
		t.Fatalf("runKeypairShow returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	if gotPath != "/os-keypairs/key-a" {
		t.Errorf("request path = %q, want /os-keypairs/key-a", gotPath)
	}
	if gotAPIVersion != "compute 2.2" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.2")
	}

	out := buf.String()
	for _, want := range []string{"key-a", "aa:bb:cc", "ssh", "u-1", "ssh-rsa AAAA", "Public Key"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunKeypairCreate_GeneratedPrintsPrivateKey(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotAPIVersion, gotPath string
	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/os-keypairs", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
  "keypair": {
    "name": "gen-key",
    "fingerprint": "11:22:33",
    "type": "ssh",
    "user_id": "u-1",
    "public_key": "ssh-rsa GEN",
    "private_key": "-----BEGIN PRIVATE KEY-----\nMII\n-----END PRIVATE KEY-----\n"
  }
}`))
	})

	client := computeClient(fakeServer, "2.2")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runKeypairCreate(context.Background(), client, o, "gen-key", &keypairCreateFlags{}, &buf); err != nil {
		t.Fatalf("runKeypairCreate returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if gotPath != "/os-keypairs" {
		t.Errorf("request path = %q, want /os-keypairs", gotPath)
	}
	if gotAPIVersion != "compute 2.2" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.2")
	}
	kp, ok := gotBody["keypair"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing 'keypair' object: %#v", gotBody)
	}
	if kp["name"] != "gen-key" {
		t.Errorf("body name = %v, want gen-key", kp["name"])
	}
	// Generated: no public_key should be sent.
	if _, present := kp["public_key"]; present {
		t.Errorf("generated create should not send public_key: %#v", kp)
	}

	// The private key is printed verbatim, without table headers.
	out := buf.String()
	if !strings.Contains(out, "-----BEGIN PRIVATE KEY-----") {
		t.Errorf("output missing private key:\n%s", out)
	}
	if strings.Contains(out, "Fingerprint") {
		t.Errorf("generated create output should be raw private key, not a table:\n%s", out)
	}
}

func TestRunKeypairCreate_ImportSendsPublicKey(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const pubKey = "ssh-rsa AAAAB3Nza imported-key"
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "id_rsa.pub")
	if err := os.WriteFile(keyFile, []byte(pubKey), 0o600); err != nil {
		t.Fatalf("writing key file: %v", err)
	}

	var gotBody map[string]any
	fakeServer.Mux.HandleFunc("/os-keypairs", func(w http.ResponseWriter, r *http.Request) {
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
  "keypair": {
    "name": "imp-key",
    "fingerprint": "44:55:66",
    "type": "ssh",
    "user_id": "u-2",
    "public_key": "` + pubKey + `"
  }
}`))
	})

	client := computeClient(fakeServer, "2.2")
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runKeypairCreate(context.Background(), client, o, "imp-key", &keypairCreateFlags{publicKey: keyFile}, &buf); err != nil {
		t.Fatalf("runKeypairCreate returned error: %v", err)
	}

	kp, ok := gotBody["keypair"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing 'keypair' object: %#v", gotBody)
	}
	if kp["public_key"] != pubKey {
		t.Errorf("body public_key = %v, want %q", kp["public_key"], pubKey)
	}

	// Imported: no private key returned, so a table is rendered.
	out := buf.String()
	for _, want := range []string{"imp-key", "44:55:66", "u-2", "Fingerprint"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunKeypairDelete_RequestMethod(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotAPIVersion, gotPath string
	fakeServer.Mux.HandleFunc("/os-keypairs/key-a", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAPIVersion = r.Header.Get("OpenStack-API-Version")
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.WriteHeader(http.StatusAccepted)
	})

	client := computeClient(fakeServer, "2.2")

	var buf bytes.Buffer
	if err := runKeypairDelete(context.Background(), client, []string{"key-a"}, &buf); err != nil {
		t.Fatalf("runKeypairDelete returned error: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("request method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/os-keypairs/key-a" {
		t.Errorf("request path = %q, want /os-keypairs/key-a", gotPath)
	}
	if gotAPIVersion != "compute 2.2" {
		t.Errorf("OpenStack-API-Version = %q, want %q", gotAPIVersion, "compute 2.2")
	}
}
