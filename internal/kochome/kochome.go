// Package kochome is the one place koc decides where configuration and state
// live on disk, and which profile it runs under.
//
// Without KOC_HOME nothing changes: koc reads clouds.yaml from openstacksdk's
// search path, ~/.vault-token and ~/.kube/config exactly as it always has.
// With KOC_HOME set, that directory replaces every implicit location — the
// working directory, $HOME, XDG_CONFIG_HOME, XDG_CONFIG_DIRS and /etc — so an
// embedding host can hand koc one directory and know koc touches nothing else.
// A path the operator names explicitly (OS_CLIENT_CONFIG_FILE, VAULT_TOKEN_FILE,
// KUBECONFIG, --kubeconfig) still wins in the default profile: it is as
// deliberate as a flag.
//
// The embedded profile (KOC_PROFILE=embedded, or a binary built with the
// koc_embedded tag) is stricter. KOC_HOME defaults to a directory under
// $TMPDIR, explicit config paths are ignored, and clouds.yaml is not read at
// all: the host supplies the scope, the user acts only as themselves.
//
// Every config read goes through Open/ReadFile so a test can observe each path
// koc opens (SetOpenHookForTest) without strace.
package kochome

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// Environment variables the resolver reads.
const (
	// EnvHome names the directory every config read and state write is
	// confined to.
	EnvHome = "KOC_HOME"
	// EnvProfile selects the profile; the only non-default value is
	// ProfileEmbedded.
	EnvProfile = "KOC_PROFILE"
	// ProfileEmbedded is the profile for running inside a sandboxed web-console
	// terminal: no prompts, no local files, no clouds.yaml, no $HOME.
	ProfileEmbedded = "embedded"
)

// embeddedHomeName is the directory KOC_HOME defaults to under $TMPDIR in the
// embedded profile.
const embeddedHomeName = "koc-home"

// Embedded reports whether koc runs under the embedded profile: always in a
// koc_embedded build, and when KOC_PROFILE=embedded otherwise.
func Embedded() bool {
	return builtEmbedded || strings.EqualFold(strings.TrimSpace(os.Getenv(EnvProfile)), ProfileEmbedded)
}

// BuiltEmbedded reports whether this binary was built with the koc_embedded
// tag, which leaves the Vault, S3 and Kubernetes code out entirely.
func BuiltEmbedded() bool { return builtEmbedded }

// Dir returns the directory koc is confined to, or "" when it is not confined
// (no KOC_HOME, default profile). A relative KOC_HOME is made absolute against
// the working directory once, here, so a later chdir cannot move it.
func Dir() string {
	if d := strings.TrimSpace(os.Getenv(EnvHome)); d != "" {
		if abs, err := filepath.Abs(d); err == nil {
			return abs
		}
		return d
	}
	if Embedded() {
		return filepath.Join(os.TempDir(), embeddedHomeName)
	}
	return ""
}

// Confined reports whether every implicit config location is replaced by Dir.
func Confined() bool { return Dir() != "" }

// UserHomeDir is os.UserHomeDir for config lookups: the confined directory when
// there is one, so ~/.vault-token or ~/.kube/config resolve inside it.
func UserHomeDir() (string, error) {
	if d := Dir(); d != "" {
		return d, nil
	}
	return os.UserHomeDir()
}

// Resolve makes a state path the host named (the history file, the audit file)
// absolute: a relative one lands inside the confined directory, or the working
// directory when there is none. An absolute path is the host's explicit choice
// and is kept.
func Resolve(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	if d := Dir(); d != "" {
		return filepath.Join(d, p)
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// ExplicitPath returns the value of an environment variable that names a config
// file explicitly, or "" when it is unset or the embedded profile ignores it.
func ExplicitPath(env string) string {
	if Embedded() {
		return ""
	}
	return os.Getenv(env)
}

// openHook, when set, observes every path Open/ReadFile/OpenFile is asked for.
var openHook atomic.Pointer[func(string)]

// SetOpenHookForTest installs fn to observe every path the resolver opens and
// returns the function that removes it. Test-only.
func SetOpenHookForTest(fn func(path string)) (restore func()) {
	openHook.Store(&fn)
	return func() { openHook.Store(nil) }
}

func notify(p string) {
	if fn := openHook.Load(); fn != nil {
		(*fn)(p)
	}
}

// Open is os.Open for a config file.
func Open(p string) (*os.File, error) {
	notify(p)
	return os.Open(p) //nolint:gosec // G304: a config path the resolver chose or the operator named
}

// ReadFile is os.ReadFile for a config file.
func ReadFile(p string) ([]byte, error) {
	notify(p)
	return os.ReadFile(p) //nolint:gosec // G304: a config path the resolver chose or the operator named
}

// OpenFile is os.OpenFile for a state file (history, audit log).
func OpenFile(p string, flag int, perm os.FileMode) (*os.File, error) {
	notify(p)
	return os.OpenFile(p, flag, perm) //nolint:gosec // G304: a state path the host named
}

// Stat is os.Stat for a config file, observed like Open.
func Stat(p string) (os.FileInfo, error) {
	notify(p)
	return os.Stat(p)
}
