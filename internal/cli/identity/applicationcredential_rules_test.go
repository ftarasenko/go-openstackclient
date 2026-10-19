package identity

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const rulesJSON = `[{"service":"compute","method":"GET","path":"/v2.1/servers"}]`

// --access-rules lands verbatim in the create body, and the rules keystone
// echoes back are rendered one per entry.
func TestRunAppCredCreate_AccessRules(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(file, []byte(rulesJSON+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, arg := range map[string]string{"inline": rulesJSON, "file": file} {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fakeServer.Mux.HandleFunc("/users/u1/application_credentials", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodPost)
				th.TestJSONRequest(t, r, `{"application_credential":{"name":"ro","unrestricted":false,
					"access_rules":[{"service":"compute","method":"GET","path":"/v2.1/servers"}]}}`)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"application_credential":{"id":"c1","name":"ro","secret":"s",
					"access_rules":[{"id":"ar1","service":"compute","method":"GET","path":"/v2.1/servers"}]}}`))
			})

			o := &output.Options{Format: output.FormatValue, Columns: []string{"Access Rules"}}
			var buf bytes.Buffer
			f := &appCredCreateFlags{accessRules: arg}
			if err := runAppCredCreate(context.Background(), identityClient(fakeServer), o, "u1", "ro", f, &buf); err != nil {
				t.Fatalf("runAppCredCreate: %v", err)
			}
			if got, want := strings.TrimSpace(buf.String()), "GET compute /v2.1/servers"; got != want {
				t.Errorf("Access Rules = %q, want %q", got, want)
			}
		})
	}
}

func TestParseAccessRules_Errors(t *testing.T) {
	for _, tc := range []struct{ arg, want string }{
		{`[{"service":"compute","method":"GET"}]`, `rule 0: needs "service", "method" and "path"`},
		{`{"service":"compute"}`, "want a JSON list"},
		{`[{"service":`, "want a JSON list"},
		{filepath.Join(t.TempDir(), "missing.json"), "neither JSON nor a readable file"},
	} {
		if _, err := parseAccessRules(tc.arg); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parseAccessRules(%q) err = %v, want %q", tc.arg, err, tc.want)
		}
	}
	// A rule naming an existing access rule by ID needs nothing else.
	if rules, err := parseAccessRules(`[{"id":"ar1"}]`); err != nil || len(rules) != 1 || rules[0].ID != "ar1" {
		t.Errorf("by ID: %v, %v", rules, err)
	}
}

func TestRunAppCredShow_AccessRules(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/users/u1/application_credentials", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"application_credentials":[{"id":"c1","name":"ro"}]}`))
	})
	fakeServer.Mux.HandleFunc("/users/u1/application_credentials/c1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"application_credential":{"id":"c1","name":"ro","access_rules":[
			{"id":"a","service":"compute","method":"GET","path":"/v2.1/servers"},
			{"id":"b","service":"image","method":"GET","path":"/v2/images/*"}]}}`))
	})
	o := &output.Options{Format: output.FormatValue, Columns: []string{"Access Rules"}}
	var buf bytes.Buffer
	if err := runAppCredShow(context.Background(), identityClient(fakeServer), o, "u1", "ro", &buf); err != nil {
		t.Fatalf("runAppCredShow: %v", err)
	}
	if got, want := strings.TrimSpace(buf.String()), "GET compute /v2.1/servers, GET image /v2/images/*"; got != want {
		t.Errorf("Access Rules = %q, want %q", got, want)
	}
}
