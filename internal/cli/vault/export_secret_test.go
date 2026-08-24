package vaultcli

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/vault"
)

// exportSecret is the per-secret half of "vault kv export". Its point is that an
// unreadable secret becomes a failed test case instead of aborting the export —
// the report exists to say which paths could and could not be read, so one
// missing secret must not cost the operator the other several hundred.
func TestExportSecret(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		path        string
		status      int
		body        string
		wantNames   []string
		wantClass   string
		wantFailure string
		wantSkipped string
	}{
		{
			name:      "a readable secret becomes one encrypted case",
			path:      "root/openrc",
			body:      `{"data":{"data":{"value":"export OS_USERNAME=admin\n"}}}`,
			wantNames: []string{"root/openrc"},
			wantClass: classKV,
		},
		{
			// Vault answers 404 both for "no such path" and "every version
			// destroyed"; the report says so in words rather than echoing a code.
			name:        "a missing secret becomes a failed case, not an error",
			path:        "root/gone",
			status:      http.StatusNotFound,
			body:        `{"errors":[]}`,
			wantNames:   []string{"root/gone"},
			wantFailure: "secret not found or has no readable version",
		},
		{
			// Vault's own error text carries no secret material, so embedding it
			// is safe and tells the operator what actually went wrong.
			name:        "a permission failure becomes a failed case carrying vault's message",
			path:        "root/denied",
			status:      http.StatusForbidden,
			body:        `{"errors":["permission denied"]}`,
			wantNames:   []string{"root/denied"},
			wantFailure: "permission denied",
		},
		{
			name:        "an empty secret is skipped, not failed",
			path:        "root/empty",
			body:        `{"data":{"data":{}}}`,
			wantNames:   []string{"root/empty"},
			wantSkipped: "empty secret",
		},
		{
			// ssl_certificates expands one case per key so each certificate is
			// separately visible and separately encrypted in the report.
			name:      "an ssl_certificates secret expands one case per key",
			path:      "root/" + sslCertsSecret,
			body:      `{"data":{"data":{"b.pem":"BBB","a.pem":"AAA"}}}`,
			wantNames: []string{"root/" + sslCertsSecret + ":a.pem", "root/" + sslCertsSecret + ":b.pem"},
			wantClass: classKVSSL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c, err := vault.New(context.Background(), vault.Config{Addr: srv.URL, Token: "t", KVMount: "kv"})
			if err != nil {
				t.Fatal(err)
			}

			cases, err := exportSecret(context.Background(), c, &key.PublicKey, "kv", tt.path)
			if err != nil {
				t.Fatalf("exportSecret() error = %v, want the failure reported as a test case", err)
			}
			if len(cases) != len(tt.wantNames) {
				t.Fatalf("exportSecret() returned %d case(s), want %d", len(cases), len(tt.wantNames))
			}
			for i, want := range tt.wantNames {
				if cases[i].Name != want {
					t.Errorf("case %d name = %q, want %q", i, cases[i].Name, want)
				}
			}
			tc := cases[0]
			switch {
			case tt.wantFailure != "":
				if tc.Failure == nil || !strings.Contains(tc.Failure.Message, tt.wantFailure) {
					t.Fatalf("case failure = %+v, want one containing %q", tc.Failure, tt.wantFailure)
				}
				if tc.SystemOut != "" {
					t.Errorf("a failed case must carry no payload, got %q", tc.SystemOut)
				}
			case tt.wantSkipped != "":
				if tc.Skipped == nil || tc.Skipped.Message != tt.wantSkipped {
					t.Fatalf("case skipped = %+v, want %q", tc.Skipped, tt.wantSkipped)
				}
			default:
				if tc.Failure != nil || tc.Skipped != nil {
					t.Fatalf("case = %+v, want a plain passing case", tc)
				}
				if tc.Classname != tt.wantClass {
					t.Errorf("classname = %q, want %q", tc.Classname, tt.wantClass)
				}
				// There is no plaintext mode: the payload must never be readable.
				if tc.SystemOut == "" || strings.Contains(tc.SystemOut, "OS_USERNAME") {
					t.Errorf("system-out = %q, want a non-empty encrypted envelope", tc.SystemOut)
				}
			}
		})
	}
}

// tally recomputes the suite attributes GitLab reads, and must be idempotent:
// runKVExport calls it once, but a stale count would misreport the whole run.
func TestJUnitSuiteTally(t *testing.T) {
	suite := junitSuite{
		Tests: 99, Failures: 99, Skipped: 99,
		Cases: []junitCase{
			{Name: "ok"},
			{Name: "boom", Failure: &junitMessage{Message: "no"}},
			{Name: "empty", Skipped: &junitMessage{Message: "empty secret"}},
			{Name: "ok2"},
			// A case carrying both counts as a failure: that arm comes first.
			{Name: "both", Failure: &junitMessage{Message: "no"}, Skipped: &junitMessage{Message: "empty secret"}},
		},
	}
	for i := range 2 {
		suite.tally()
		if suite.Tests != 5 || suite.Failures != 2 || suite.Skipped != 1 {
			t.Fatalf("after tally #%d: tests=%d failures=%d skipped=%d, want 5/2/1",
				i+1, suite.Tests, suite.Failures, suite.Skipped)
		}
	}

	empty := junitSuite{Tests: 7, Failures: 7, Skipped: 7}
	empty.tally()
	if empty.Tests != 0 || empty.Failures != 0 || empty.Skipped != 0 {
		t.Errorf("empty suite tallied to %d/%d/%d, want 0/0/0", empty.Tests, empty.Failures, empty.Skipped)
	}
}
