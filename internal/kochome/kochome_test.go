package kochome

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDir(t *testing.T) {
	t.Setenv(EnvProfile, "")
	t.Setenv(EnvHome, "")
	if d := Dir(); d != "" {
		t.Errorf("no KOC_HOME, default profile: Dir() = %q, want unconfined", d)
	}
	if Confined() {
		t.Error("Confined() without KOC_HOME")
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

func TestCheckEmbeddedHome(t *testing.T) {
	embedded := func(t *testing.T) string {
		t.Helper()
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)
		t.Setenv(EnvProfile, ProfileEmbedded)
		t.Setenv(EnvHome, "")
		return filepath.Join(tmp, embeddedHomeName)
	}

	t.Run("default profile is a no-op", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)
		t.Setenv(EnvProfile, "")
		t.Setenv(EnvHome, "")
		if err := CheckEmbeddedHome(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(tmp, embeddedHomeName)); !os.IsNotExist(err) {
			t.Errorf("default profile created the embedded home: %v", err)
		}
	})

	t.Run("KOC_HOME named by the host is trusted", func(t *testing.T) {
		dir := embedded(t)
		t.Setenv(EnvHome, t.TempDir())
		if err := CheckEmbeddedHome(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("created %s although KOC_HOME is set: %v", dir, err)
		}
	})

	t.Run("missing is created 0700", func(t *testing.T) {
		dir := embedded(t)
		if err := CheckEmbeddedHome(); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
			t.Errorf("mode = %v, want 0700", fi.Mode().Perm())
		}
		if err := CheckEmbeddedHome(); err != nil {
			t.Errorf("second call on the directory it created: %v", err)
		}
	})

	t.Run("a symlink is refused", func(t *testing.T) {
		dir := embedded(t)
		if err := os.Symlink(t.TempDir(), dir); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := CheckEmbeddedHome(); err == nil || !strings.Contains(err.Error(), EnvHome) {
			t.Errorf("symlinked home: err = %v, want a refusal naming %s", err, EnvHome)
		}
	})

	t.Run("a file is refused", func(t *testing.T) {
		dir := embedded(t)
		if err := os.WriteFile(dir, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := CheckEmbeddedHome(); err == nil {
			t.Error("a regular file was accepted as the home")
		}
	})

	t.Run("group or world writable is refused", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("permission bits do not describe who may write on Windows")
		}
		dir := embedded(t)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		err := CheckEmbeddedHome()
		if err == nil || !strings.Contains(err.Error(), "writable by group or others") {
			t.Errorf("0777 home: err = %v, want a refusal", err)
		}
	})
}
