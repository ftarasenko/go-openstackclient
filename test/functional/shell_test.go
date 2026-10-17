//go:build functional

package functional

import (
	"encoding/json"
	"strings"
	"testing"
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
