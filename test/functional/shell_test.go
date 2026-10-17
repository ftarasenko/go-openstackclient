//go:build functional

package functional

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// `koc shell` with its lines piped in, as a script would run it: every line
// runs on a fresh command tree, a failing line does not end the session, and
// the session itself exits 0.
func TestShell(t *testing.T) {
	c := ft(t)
	r := defaultRunner(c)
	want := issue(t, r)

	res := r.withStdin(strings.Join([]string{
		"koc token issue -f json", // a pasted "koc " prefix is dropped
		"server lst",              // fails; the shell goes on
		"network list --long -f json -c ID -c Name", // --long must not leak …
		"network list -f json -c ID -c Name",        // … into this line
		"exit",
		"token issue", // after exit: never runs
	}, "\n")+"\n").run(t, "shell")
	if res.code != 0 {
		t.Fatalf("koc shell: exit %d\nstderr: %s", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, `unknown command "lst"`) {
		t.Errorf("the failing line was not reported:\n%s", res.stderr)
	}

	dec := json.NewDecoder(strings.NewReader(res.stdout))
	var tok map[string]any
	if err := dec.Decode(&tok); err != nil {
		t.Fatalf("token issue: %v\n%s", err, res.stdout)
	}
	if str(tok["project_id"]) != want.project || str(tok["user_id"]) != want.user {
		t.Errorf("the shell scoped to %v/%v, want %+v", tok["project_id"], tok["user_id"], want)
	}
	var long, plain []map[string]any
	if err := dec.Decode(&long); err != nil {
		t.Fatalf("network list --long: %v\n%s", err, res.stdout)
	}
	if err := dec.Decode(&plain); err != nil {
		t.Fatalf("network list: %v\n%s", err, res.stdout)
	}
	if len(long) != len(plain) || len(plain) == 0 {
		t.Errorf("network list returned %d rows, --long %d", len(plain), len(long))
	}
	if dec.More() {
		t.Errorf("output after exit:\n%s", res.stdout)
	}
}

// TestShellScopeFile: the embedding host's path — no credentials in the
// environment at all, only a scope file holding a token koc issued itself —
// against a real Keystone. Each line re-authenticates with the token, scoped
// to the file's project.
func TestShellScopeFile(t *testing.T) {
	c := ft(t)
	var tok map[string]any
	defaultRunner(c).json(t, &tok, "token", "issue")
	expires, err := time.Parse(time.RFC3339, str(tok["expires"]))
	if err != nil {
		t.Fatalf("token issue: expires %q: %v", tok["expires"], err)
	}
	scope := map[string]any{
		"auth_url":   openrcValue(c, "OS_AUTH_URL"),
		"token":      str(tok["id"]),
		"region":     openrcValue(c, "OS_REGION_NAME"),
		"project_id": str(tok["project_id"]),
		"expires_at": expires.UTC().Format(time.RFC3339),
	}
	if ca := openrcValue(c, "OS_CACERT"); ca != "" {
		scope["cacert"] = ca
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "scope.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	r := runner{env: append(baseEnv(), "KOC_SCOPE_FILE="+path, "KOC_HOME="+t.TempDir())}
	res := r.withStdin("token issue -f json\nscope\n").run(t, "shell")
	if res.code != 0 {
		t.Fatalf("koc shell: exit %d\nstderr: %s", res.code, res.stderr)
	}
	var got map[string]any
	if err := json.NewDecoder(strings.NewReader(res.stdout)).Decode(&got); err != nil {
		t.Fatalf("token issue: %v\n%s", err, res.stdout)
	}
	if str(got["project_id"]) != str(tok["project_id"]) || str(got["user_id"]) != str(tok["user_id"]) {
		t.Errorf("scoped to %v/%v, want %v/%v", got["project_id"], got["user_id"], tok["project_id"], tok["user_id"])
	}
	if strings.Contains(res.stdout+res.stderr, str(tok["id"])) {
		t.Error("the session printed the scope file's token")
	}
}
