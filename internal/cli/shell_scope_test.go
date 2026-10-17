package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ftarasenko/go-openstackclient/internal/cli/shell"
	"github.com/ftarasenko/go-openstackclient/internal/kochome"
)

// regionCloud is a mock Keystone whose catalog has one nova per region
// (/R1/v2.1, /R2/v2.1, /R3/v2.1). It records how every authentication was
// made and which region every list reached.
type regionCloud struct {
	t      *testing.T
	base   string
	tokens map[string]bool // the token IDs Keystone accepts

	mu    sync.Mutex
	auths []authReq
	hits  []string
}

type authReq struct {
	methods []string
	token   string
	project string
}

func newRegionCloud(t *testing.T, tokens ...string) *regionCloud {
	t.Helper()
	rc := &regionCloud{t: t, tokens: map[string]bool{}}
	for _, tok := range tokens {
		rc.tokens[tok] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/auth/tokens", rc.auth)
	mux.HandleFunc("GET /{region}/v2.1/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		region := r.PathValue("region")
		rc.mu.Lock()
		rc.hits = append(rc.hits, region)
		rc.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"servers":[{"id":"11111111-1111-1111-1111-111111111111","name":"web-%s","status":"ACTIVE","addresses":{},"flavor":{"original_name":"m1"}}]}`, region)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	rc.base = srv.URL
	return rc
}

func (rc *regionCloud) auth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Auth struct {
			Identity struct {
				Methods []string `json:"methods"`
				Token   struct {
					ID string `json:"id"`
				} `json:"token"`
			} `json:"identity"`
			Scope struct {
				Project struct {
					ID string `json:"id"`
				} `json:"project"`
			} `json:"scope"`
		} `json:"auth"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		rc.t.Errorf("auth body: %v", err)
	}
	id := body.Auth.Identity
	rc.mu.Lock()
	rc.auths = append(rc.auths, authReq{methods: id.Methods, token: id.Token.ID, project: body.Auth.Scope.Project.ID})
	rc.mu.Unlock()
	if !rc.tokens[id.Token.ID] {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"The request you have made requires authentication."}}`))
		return
	}
	var eps []string
	for _, region := range []string{"R1", "R2", "R3"} {
		eps = append(eps, fmt.Sprintf(`{"id":"e-%[1]s","interface":"public","region":"%[1]s","region_id":"%[1]s","url":"%[2]s/%[1]s/v2.1"}`, region, rc.base))
	}
	w.Header().Set("X-Subject-Token", "scoped-"+id.Token.ID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = fmt.Fprintf(w, `{"token":{"methods":["token"],"expires_at":"2099-01-01T00:00:00.000000Z",`+
		`"project":{"id":%q,"name":"p","domain":{"id":"default","name":"Default"}},`+
		`"user":{"id":"u1","name":"alice","domain":{"id":"default","name":"Default"}},`+
		`"roles":[{"id":"r1","name":"member"}],`+
		`"catalog":[{"type":"compute","name":"nova","id":"c1","endpoints":[%s]}]}}`,
		body.Auth.Scope.Project.ID, strings.Join(eps, ","))
}

func (rc *regionCloud) take() ([]authReq, []string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	a, h := rc.auths, rc.hits
	rc.auths, rc.hits = nil, nil
	return a, h
}

// writeScope writes a scope file, moving its mtime so a rewrite within the
// filesystem's timestamp granularity is still seen as one.
func writeScope(t *testing.T, path, base, token, region, project string, expires time.Time) {
	t.Helper()
	doc := fmt.Sprintf(`{"auth_url":%q,"token":%q,"region":%q,"project_id":%q,"project_name":%q,"user_name":"alice","expires_at":%q}`,
		base+"/v3", token, region, project, "name-"+project, expires.UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	bump := time.Now().Add(time.Duration(len(token)+len(region)) * time.Second)
	if err := os.Chtimes(path, bump, bump); err != nil {
		t.Fatal(err)
	}
}

// steppedInput hands the piped shell one line per Read, running the step
// registered for that line first — so a test can rewrite the scope file
// between two lines of one session.
type steppedInput struct {
	lines []string
	steps map[int]func()
	i     int
}

func (s *steppedInput) Read(p []byte) (int, error) {
	if s.i >= len(s.lines) {
		return 0, io.EOF
	}
	if fn := s.steps[s.i]; fn != nil {
		fn()
	}
	n := copy(p, s.lines[s.i]+"\n")
	s.i++
	return n, nil
}

func TestShell_ScopeFile(t *testing.T) {
	if kochome.BuiltEmbedded() {
		t.Skip("types --os-region-name, which the embedded profile refuses; see TestShell_EmbeddedScopeFlags")
	}
	rc := newRegionCloud(t, "tokA", "tokB")
	clearOSEnv(t)
	// A full password openrc in the environment, pointing elsewhere: the scope
	// file must replace all of it, not merge with it.
	t.Setenv("OS_AUTH_URL", "https://elsewhere.example.com/v3")
	t.Setenv("OS_USERNAME", "admin")
	t.Setenv("OS_PASSWORD", "env-password")
	t.Setenv("OS_PROJECT_NAME", "admin")
	t.Setenv("OS_REGION_NAME", "R3")
	t.Setenv("NO_COLOR", "1")
	path := filepath.Join(t.TempDir(), "scope.json")
	t.Setenv("KOC_SCOPE_FILE", path)
	future := time.Now().Add(time.Hour)
	writeScope(t, path, rc.base, "tokA", "R1", "projA", future)

	var out, errOut bytes.Buffer
	in := &steppedInput{
		lines: []string{
			"server list",
			"server list", // after the rotation below
			"server list --os-region-name R3",
			"server list", // after expiry
			"scope",
			"server list", // after the file broke
		},
		steps: map[int]func(){
			1: func() { writeScope(t, path, rc.base, "tokB", "R2", "projB", future) },
			3: func() { writeScope(t, path, rc.base, "tokB", "R2", "projB", time.Now().Add(-time.Minute)) },
			5: func() {
				if err := os.WriteFile(path, []byte(`{"token":"tokB",`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	if err := shell.Run(context.Background(), shellConfig("test"), in, &out, &errOut, nil); err != nil {
		t.Fatal(err)
	}

	auths, hits := rc.take()
	wantAuths := []authReq{
		{methods: []string{"token"}, token: "tokA", project: "projA"},
		{methods: []string{"token"}, token: "tokB", project: "projB"},
		{methods: []string{"token"}, token: "tokB", project: "projB"},
	}
	if fmt.Sprint(auths) != fmt.Sprint(wantAuths) {
		t.Errorf("authentications:\n got %v\nwant %v", auths, wantAuths)
	}
	// R1 from the first file, R2 after the rotation, R3 typed on the line;
	// nothing once expired or broken.
	if got := strings.Join(hits, ","); got != "R1,R2,R3" {
		t.Errorf("regions reached: %s, want R1,R2,R3", got)
	}

	stderr := errOut.String()
	for _, want := range []string{
		"scope: R2 · name-projB",
		"the session expired at",
		"the console will renew it",
		"not valid JSON (truncated)",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if !strings.Contains(out.String(), "token expires: ") || !strings.Contains(out.String(), "(expired)") {
		t.Errorf("scope did not show the expiry:\n%s", out.String())
	}
	for _, secret := range []string{"tokA", "tokB", "env-password"} {
		if strings.Contains(out.String()+stderr, secret) {
			t.Errorf("the session printed %q", secret)
		}
	}
}

// TestShell_ScopeTokenRedactedUnderDebug: --debug dumps every request,
// including the token-auth POST that carries the scope file's token in its
// body and the X-Auth-Token header on every call after it.
func TestShell_ScopeTokenRedactedUnderDebug(t *testing.T) {
	rc := newRegionCloud(t, "gAAAAAscope-file-token")
	clearOSEnv(t)
	path := filepath.Join(t.TempDir(), "scope.json")
	t.Setenv("KOC_SCOPE_FILE", path)
	writeScope(t, path, rc.base, "gAAAAAscope-file-token", "R1", "projA", time.Now().Add(time.Hour))

	var out, errOut bytes.Buffer
	dump := captureStderr(t, func() {
		err := shell.Run(context.Background(), shellConfig("test"), strings.NewReader("server list --debug\n"), &out, &errOut, nil)
		if err != nil {
			t.Error(err)
		}
	})
	if auths, _ := rc.take(); len(auths) != 1 {
		t.Fatalf("authenticated %d times, want 1; stderr:\n%s", len(auths), errOut.String())
	}
	if !strings.Contains(dump, "POST") || !strings.Contains(dump, "/v3/auth/tokens") {
		t.Fatalf("--debug dumped no auth request:\n%s", dump)
	}
	if !strings.Contains(dump, `"methods"`) {
		t.Fatalf("--debug elided the auth body, so the test proves nothing:\n%s", dump)
	}
	if strings.Contains(dump+out.String()+errOut.String(), "scope-file-token") {
		t.Errorf("--debug leaked the scope file's token:\n%s", dump)
	}
}

// TestShell_EmbeddedScopeFlags: in the embedded profile --os-region-name and
// --os-project-id are accepted only with the scope file's own values.
func TestShell_EmbeddedScopeFlags(t *testing.T) {
	rc := newRegionCloud(t, "tokA")
	clearOSEnv(t)
	t.Setenv("OS_REGION_NAME", "R3") // the environment is not the session
	t.Setenv("KOC_PROFILE", "embedded")
	t.Setenv("KOC_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "scope.json")
	t.Setenv("KOC_SCOPE_FILE", path)
	writeScope(t, path, rc.base, "tokA", "R1", "projA", time.Now().Add(time.Hour))

	var out, errOut bytes.Buffer
	lines := "server list --os-region-name R1 --os-project-id projA\n" +
		"server list --os-region-name R3\n" +
		"server list --os-project-id projB\n" +
		"server list --os-token tokB\n"
	if err := shell.Run(context.Background(), shellConfig("test"), strings.NewReader(lines), &out, &errOut, nil); err != nil {
		t.Fatal(err)
	}
	if _, hits := rc.take(); strings.Join(hits, ",") != "R1" {
		t.Errorf("regions reached: %v, want only the matching line's R1", hits)
	}
	for _, want := range []string{
		"--os-region-name R3 is not this session's region; switch region in the console",
		"--os-project-id projB is not this session's project",
		"--os-token is not available in the console terminal",
	} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut.String())
		}
	}
}

// TestShell_AuditThroughTheTree: the audit event records the expanded argv
// and the resolved command path, with the embedded profile's refusal reason.
func TestShell_AuditThroughTheTree(t *testing.T) {
	rc := newRegionCloud(t, "tokA")
	clearOSEnv(t)
	t.Setenv("KOC_PROFILE", "embedded")
	t.Setenv("KOC_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "scope.json")
	t.Setenv("KOC_SCOPE_FILE", path)
	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv("KOC_AUDIT_FILE", audit)
	writeScope(t, path, rc.base, "tokA", "R1", "projA", time.Now().Add(time.Hour))

	in := &steppedInput{
		lines: []string{"server li --os-token tokB", "server li --lo", "image save x", "server list"},
		steps: map[int]func(){
			3: func() { writeScope(t, path, rc.base, "tokA", "R1", "projA", time.Now().Add(-time.Minute)) },
		},
	}
	var out, errOut bytes.Buffer
	if err := shell.Run(context.Background(), shellConfig("test"), in, &out, &errOut, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tokB") || strings.Contains(string(raw), "web-R1") {
		t.Fatalf("the audit log holds a token or command output:\n%s", raw)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev struct {
			Argv      []string `json:"argv"`
			Command   string   `json:"command"`
			Region    string   `json:"region"`
			ProjectID string   `json:"project_id"`
			ExitCode  int      `json:"exit_code"`
			Refused   string   `json:"refused"`
		}
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		got = append(got, fmt.Sprintf("%q %s %s/%s %d %s", ev.Argv, ev.Command, ev.Region, ev.ProjectID, ev.ExitCode, ev.Refused))
	}
	want := []string{
		`["server" "list" "--os-token" "***"] server list R1/projA 1 credential`,
		`["server" "list" "--long"] server list R1/projA 0 `,
		`["image" "save" "x"] image save R1/projA 1 file`,
		`["server" "list"] server list R1/projA 1 expired`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("audit events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
