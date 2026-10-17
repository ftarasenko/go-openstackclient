package shell

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/kochome"
)

// Scope precedence inside the shell is: flags typed on the line, then the
// scope file (KOC_SCOPE_FILE), then the OS_* environment read at start. The
// scope file is applied to each line's fresh tree as flag *values* without
// marking them changed — exactly how an exported OS_* variable reaches a flag
// — so anything typed on the line still wins, and nothing downstream can tell
// a scope-file value from an environment one.

// SessionAnnotation prefixes the root annotations recording the session's own
// value of a scope flag, so the embedded profile can tell "--os-region-name
// <the region the session is in>" from a switch.
const SessionAnnotation = "koc-session:"

// scopeFile tracks KOC_SCOPE_FILE between lines.
type scopeFile struct {
	path string
	// The last stat and content hash, to skip re-reading an unchanged file.
	modTime time.Time
	size    int64
	sum     [sha256.Size]byte
	// current is the last scope that parsed; err is why the file is unusable
	// now. Lines are refused while err is set: a half-written or broken file
	// must not fall back to whatever the environment says.
	current *auth.ScopeFile
	err     error
	loaded  bool
}

func newScopeFile() *scopeFile {
	p := os.Getenv(auth.EnvScopeFile)
	if p == "" {
		return nil
	}
	return &scopeFile{path: kochome.Resolve(p)}
}

// refresh re-reads the file when its stat or content changed and reports
// whether the scope it describes changed.
func (f *scopeFile) refresh() (changed bool) {
	st, err := kochome.Stat(f.path)
	if err != nil {
		f.err = fmt.Errorf("scope file: %w", err)
		return false
	}
	if f.loaded && f.err == nil && st.ModTime().Equal(f.modTime) && st.Size() == f.size {
		return false
	}
	raw, err := kochome.ReadFile(f.path)
	if err != nil {
		f.err = fmt.Errorf("scope file: %w", err)
		return false
	}
	f.modTime, f.size = st.ModTime(), st.Size()
	sum := sha256.Sum256(raw)
	if f.loaded && f.err == nil && bytes.Equal(sum[:], f.sum[:]) {
		return false
	}
	sf, err := auth.ParseScopeFile(raw)
	if err != nil {
		f.err = fmt.Errorf("scope file %s: %w", f.path, err)
		return false
	}
	prev := f.current
	f.sum, f.current, f.err, f.loaded = sum, sf, nil, true
	return prev != nil && (prev.Region != sf.Region || prev.ProjectID != sf.ProjectID)
}

// info is the scope file as the prompt and `scope` show it.
func (f *scopeFile) info() scopeInfo {
	sf := f.current
	if sf == nil {
		return scopeInfo{}
	}
	return scopeInfo{
		authURL:     sf.AuthURL,
		region:      sf.Region,
		projectID:   sf.ProjectID,
		projectName: sf.ProjectName,
		userName:    sf.UserName,
		expiresAt:   sf.ExpiresAt,
	}
}

// apply sets the scope file's auth options on a line's tree, as values the
// line's own flags can still override, and records the session's scope.
func (f *scopeFile) apply(root *cobra.Command) {
	if f == nil || f.current == nil {
		return
	}
	pf := root.PersistentFlags()
	for name, val := range f.current.FlagValues() {
		if fl := pf.Lookup(name); fl != nil {
			_ = fl.Value.Set(val)
		}
	}
	setSession(root, f.current.Region, f.current.ProjectID)
}

// setSession records the session's region and project on root for the
// embedded profile's guard.
func setSession(root *cobra.Command, region, projectID string) {
	if root.Annotations == nil {
		root.Annotations = map[string]string{}
	}
	root.Annotations[SessionAnnotation+"os-region-name"] = region
	root.Annotations[SessionAnnotation+"os-project-id"] = projectID
}

// checkLine reports why a koc command may not run now: the scope file is
// unusable, or its token has expired. "" means it may.
func (f *scopeFile) checkLine(now time.Time) (reason string, err error) {
	if f == nil {
		return "", nil
	}
	switch {
	case f.err != nil:
		return "scope", f.err
	case f.current == nil:
		return "scope", errors.New("scope file not read yet")
	}
	if f.current.Expired(now) {
		return "expired", fmt.Errorf("the session expired at %s; the console will renew it",
			f.current.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return "", nil
}
