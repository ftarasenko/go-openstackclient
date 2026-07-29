package vaultcli

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/vault"
)

// exportFixture serves a subtree with one ordinary secret, one ssl_certificates
// secret, one empty secret and one that cannot be read.
func exportFixture(t *testing.T) *vault.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/kv/metadata/dev":
			_, _ = w.Write([]byte(`{"data":{"keys":["openrc","ssl_certificates","empty","denied"]}}`))
		case "/v1/kv/data/dev/openrc":
			_, _ = w.Write([]byte(`{"data":{"data":{"OS_PASSWORD":"s3cret","OS_USERNAME":"admin"}}}`))
		case "/v1/kv/data/dev/ssl_certificates":
			_, _ = w.Write([]byte(`{"data":{"data":{"backend_pem":"-----BEGIN CERTIFICATE-----\nAAA\n","backend_key_pem":"-----BEGIN PRIVATE KEY-----\nBBB\n","unused":""}}}`))
		case "/v1/kv/data/dev/empty":
			_, _ = w.Write([]byte(`{"data":{"data":{}}}`))
		case "/v1/kv/data/dev/denied":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
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

func TestRunKVExport(t *testing.T) {
	c := exportFixture(t)
	var buf bytes.Buffer
	if err := runKVExport(context.Background(), c, &testKey.PublicKey, "dev", &buf); err != nil {
		t.Fatal(err)
	}
	doc := buf.String()

	// No secret material anywhere in the report, including the cert bodies and the
	// key names of the ordinary secret's data.
	for _, leak := range []string{"s3cret", "admin", "BEGIN CERTIFICATE", "BEGIN PRIVATE KEY", "AAA", "BBB", "OS_PASSWORD"} {
		if strings.Contains(doc, leak) {
			t.Errorf("report leaked %q:\n%s", leak, doc)
		}
	}
	if !strings.HasPrefix(doc, "<?xml") {
		t.Errorf("report is missing the XML declaration:\n%s", doc)
	}

	var suite junitSuite
	if err := xml.Unmarshal(buf.Bytes(), &suite); err != nil {
		t.Fatalf("report is not parseable XML: %v\n%s", err, doc)
	}
	if suite.Name != "vault:dev" {
		t.Errorf("suite name = %q", suite.Name)
	}
	// openrc + 3 ssl keys + empty + denied.
	if suite.Tests != 6 || len(suite.Cases) != 6 {
		t.Errorf("tests = %d (%d cases), want 6", suite.Tests, len(suite.Cases))
	}
	if suite.Failures != 1 {
		t.Errorf("failures = %d, want 1 (the unreadable secret)", suite.Failures)
	}
	// The empty secret plus the ssl_certificates key with an empty value.
	if suite.Skipped != 2 {
		t.Errorf("skipped = %d, want 2", suite.Skipped)
	}

	byName := map[string]junitCase{}
	for _, tc := range suite.Cases {
		byName[tc.Name] = tc
	}

	openrc, ok := byName["dev/openrc"]
	if !ok || openrc.Classname != classKV || openrc.SystemOut == "" {
		t.Fatalf("dev/openrc case = %+v", openrc)
	}
	plaintext, err := decryptPayload(testKey, "dev/openrc", openrc.SystemOut)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plaintext), `"OS_PASSWORD": "s3cret"`) {
		t.Errorf("decrypted payload = %s", plaintext)
	}

	// ssl_certificates is expanded per key, each encrypted separately.
	cert, ok := byName["dev/ssl_certificates:backend_pem"]
	if !ok || cert.Classname != classKVSSL {
		t.Fatalf("cert case = %+v", cert)
	}
	if got, err := decryptPayload(testKey, cert.Name, cert.SystemOut); err != nil || !strings.Contains(string(got), "BEGIN CERTIFICATE") {
		t.Errorf("cert payload = %s, err = %v", got, err)
	}
	if empty := byName["dev/ssl_certificates:unused"]; empty.Skipped == nil {
		t.Errorf("an empty cert value should be skipped, got %+v", empty)
	}
	if e := byName["dev/empty"]; e.Skipped == nil || e.SystemOut != "" {
		t.Errorf("empty secret case = %+v, want skipped with no payload", e)
	}
	if d := byName["dev/denied"]; d.Failure == nil || !strings.Contains(d.Failure.Message, "permission denied") {
		t.Errorf("unreadable secret case = %+v, want a failure naming the Vault error", d)
	}
}

func TestRunKVExport_LeafPath(t *testing.T) {
	c := exportFixture(t)
	var buf bytes.Buffer
	if err := runKVExport(context.Background(), c, &testKey.PublicKey, "dev/openrc", &buf); err != nil {
		t.Fatal(err)
	}
	var suite junitSuite
	if err := xml.Unmarshal(buf.Bytes(), &suite); err != nil {
		t.Fatal(err)
	}
	if suite.Tests != 1 || suite.Cases[0].Name != "dev/openrc" {
		t.Errorf("suite = %+v, want the single leaf secret", suite)
	}
}

func TestRunKVDecrypt_RoundTrip(t *testing.T) {
	c := exportFixture(t)
	var report bytes.Buffer
	if err := runKVExport(context.Background(), c, &testKey.PublicKey, "dev", &report); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	o := &output.Options{Format: output.FormatCSV}
	if err := runKVDecrypt(bytes.NewReader(report.Bytes()), testKey, o, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"Path,Key,Value",
		"dev/openrc,OS_PASSWORD,s3cret",
		"dev/openrc,OS_USERNAME,admin",
		"dev/ssl_certificates,backend_pem,",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("decrypted output missing %q:\n%s", want, got)
		}
	}
	// Skipped and failed cases carry no payload and must not appear.
	for _, absent := range []string{"dev/empty", "dev/denied", "unused"} {
		if strings.Contains(got, absent) {
			t.Errorf("output should not mention %q:\n%s", absent, got)
		}
	}
}

func TestRunKVDecrypt_Rejections(t *testing.T) {
	t.Run("not an export", func(t *testing.T) {
		doc := `<?xml version="1.0"?><testsuite name="other" tests="1">` +
			`<testcase classname="pytest" name="t" time="0.1"></testcase></testsuite>`
		err := runKVDecrypt(strings.NewReader(doc), testKey, &output.Options{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "no encrypted payloads") {
			t.Errorf("err = %v, want a not-an-export error", err)
		}
	})

	t.Run("payload moved between cases", func(t *testing.T) {
		c := exportFixture(t)
		var report bytes.Buffer
		if err := runKVExport(context.Background(), c, &testKey.PublicKey, "dev/openrc", &report); err != nil {
			t.Fatal(err)
		}
		// Renaming the case breaks the additional authenticated data.
		tampered := strings.Replace(report.String(), `name="dev/openrc"`, `name="dev/elsewhere"`, 1)
		err := runKVDecrypt(strings.NewReader(tampered), testKey, &output.Options{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "authenticating payload") {
			t.Errorf("err = %v, want an authentication failure", err)
		}
	})
}
