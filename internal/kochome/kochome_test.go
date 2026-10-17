package kochome

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDir(t *testing.T) {
	t.Setenv(EnvProfile, "")
	t.Setenv(EnvHome, "")
	if !builtEmbedded {
		if d := Dir(); d != "" {
			t.Errorf("no KOC_HOME, default profile: Dir() = %q, want unconfined", d)
		}
		if Confined() {
			t.Error("Confined() without KOC_HOME")
		}
	}

	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv(EnvProfile, "Embedded")
	if !Embedded() {
		t.Fatal("KOC_PROFILE=Embedded is not the embedded profile")
	}
	if got, want := Dir(), filepath.Join(tmp, embeddedHomeName); got != want {
		t.Errorf("embedded default: Dir() = %q, want %q", got, want)
	}

	home := t.TempDir()
	t.Setenv(EnvHome, home)
	if got := Dir(); got != home {
		t.Errorf("KOC_HOME: Dir() = %q, want %q", got, home)
	}
	if got, _ := UserHomeDir(); got != home {
		t.Errorf("UserHomeDir() = %q, want %q", got, home)
	}
	if got, want := Resolve("history"), filepath.Join(home, "history"); got != want {
		t.Errorf("Resolve(relative) = %q, want %q", got, want)
	}
	if got := Resolve("/var/log/koc"); got != "/var/log/koc" {
		t.Errorf("Resolve(absolute) = %q, want it kept", got)
	}
	t.Setenv("VAULT_TOKEN_FILE", "/x")
	if got := ExplicitPath("VAULT_TOKEN_FILE"); got != "" {
		t.Errorf("embedded ExplicitPath = %q, want it ignored", got)
	}
}

func TestOpenHook(t *testing.T) {
	var seen []string
	restore := SetOpenHookForTest(func(p string) { seen = append(seen, p) })
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(p); err != nil {
		t.Fatal(err)
	}
	restore()
	_, _ = ReadFile(p)
	if len(seen) != 1 || seen[0] != p {
		t.Errorf("hook saw %q, want exactly one %q", seen, p)
	}
}
