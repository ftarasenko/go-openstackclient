package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ftarasenko/go-openstackclient/internal/kochome"
)

// A scope file is how an embedding host — a web console running `koc shell`
// for a signed-in user — hands koc that user's Keystone token and scope, and
// rotates them without restarting koc. KOC_SCOPE_FILE names it; `koc shell`
// re-reads it before every line.
//
// It maps onto exactly the auth options the OS_* environment sets: token auth
// (OS_AUTH_TYPE=v3token, OS_TOKEN), OS_AUTH_URL, OS_REGION_NAME,
// OS_PROJECT_ID and OS_CACERT. Every other credential or scope option is
// cleared, so a leftover OS_PASSWORD or OS_CLOUD in the environment cannot
// turn the session into somebody else's. It is the only secret on disk; the
// token never reaches a log, an error or the screen.

// EnvScopeFile names the scope file.
const EnvScopeFile = "KOC_SCOPE_FILE"

// ScopeToken is a token read from a scope file. It formats as "***", so a
// ScopeFile can be printed with %v or %+v without leaking it.
type ScopeToken string

// String hides the token.
func (ScopeToken) String() string { return "***" }

// GoString hides the token from %#v too.
func (ScopeToken) GoString() string { return `"***"` }

// ScopeFile is the scope file's JSON document.
type ScopeFile struct {
	AuthURL     string     `json:"auth_url"`
	Token       ScopeToken `json:"token"`
	Region      string     `json:"region"`
	ProjectID   string     `json:"project_id"`
	ProjectName string     `json:"project_name,omitempty"`
	UserName    string     `json:"user_name,omitempty"`
	ExpiresAt   time.Time  `json:"expires_at"`
	// CACert is a path to a CA bundle for the auth URL and the catalog
	// endpoints, the scope file's OS_CACERT.
	CACert string `json:"cacert,omitempty"`
}

// ParseScopeFile decodes and checks a scope file's content. Errors never quote
// the content: it holds the token.
func ParseScopeFile(raw []byte) (*ScopeFile, error) {
	var sf ScopeFile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sf); err != nil {
		var syn *json.SyntaxError
		var typ *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syn):
			return nil, fmt.Errorf("not valid JSON (at byte %d)", syn.Offset)
		case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
			// Usually a file read while the host was still writing it.
			return nil, errors.New("not valid JSON (truncated)")
		case errors.As(err, &typ):
			return nil, fmt.Errorf("field %q has the wrong type", typ.Field)
		}
		// Unknown-field and time-format errors name the field, not its value.
		return nil, fmt.Errorf("invalid: %s", firstLine(err.Error()))
	}
	var missing []string
	for _, f := range []struct{ name, val string }{
		{"auth_url", sf.AuthURL},
		{"token", string(sf.Token)},
		{"project_id", sf.ProjectID},
	} {
		if strings.TrimSpace(f.val) == "" {
			missing = append(missing, f.name)
		}
	}
	if sf.ExpiresAt.IsZero() {
		missing = append(missing, "expires_at")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing %s", strings.Join(missing, ", "))
	}
	return &sf, nil
}

// ReadScopeFile reads and parses the scope file at path.
func ReadScopeFile(path string) (*ScopeFile, error) {
	raw, err := kochome.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sf, err := ParseScopeFile(raw)
	if err != nil {
		return nil, fmt.Errorf("scope file %s: %w", path, err)
	}
	return sf, nil
}

// Expired reports whether the token's expiry has passed at now.
func (sf *ScopeFile) Expired(now time.Time) bool { return !now.Before(sf.ExpiresAt) }

// FlagValues returns the global auth flags the scope file sets, as the values
// their OS_* variables would have given them: token auth with the file's
// token, URL, region, project and CA bundle, and every other credential and
// scope option empty.
func (sf *ScopeFile) FlagValues() map[string]string {
	v := map[string]string{
		flagOSAuthType:   authTypeToken,
		"os-auth-url":    sf.AuthURL,
		flagOSToken:      string(sf.Token),
		"os-region-name": sf.Region,
		flagOSProjectID:  sf.ProjectID,
		"os-cacert":      sf.CACert,
	}
	for _, name := range scopeClearedFlags {
		v[name] = ""
	}
	return v
}

// scopeClearedFlags are the credential and scope options a scope file
// overrides with nothing: the token is the user's only credential, and the
// project ID is their only scope.
var scopeClearedFlags = []string{
	"os-cloud",
	"os-username", "os-user-id",
	flagOSPassword, "os-passcode",
	flagOSProjectName,
	"os-project-domain-name", "os-project-domain-id",
	"os-user-domain-name", "os-user-domain-id",
	"os-domain-name", "os-domain-id",
	"os-system-scope", "os-trust-id", "os-endpoint",
	"os-application-credential-id", "os-application-credential-name", "os-application-credential-secret",
	"os-cert", "os-key",
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}
