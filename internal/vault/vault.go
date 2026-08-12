// Package vault is a dependency-free, minimal HashiCorp Vault client. It exists
// so koc can fetch an openrc-style KV v2 secret and authenticate the normal
// Keystone flow from it, and so `koc vault kv` can read/list/copy KV v2 secrets,
// without vendoring the Vault SDK (honoring the repo's air-gap /
// minimal-dependency invariant). It supports AppRole login (or a pre-issued
// token), the KV v2 read/write/list API, and Vault Enterprise namespaces via the
// X-Vault-Namespace header.
package vault

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// ErrNotFound is returned for a 404. Vault answers 404 both for a missing secret
// and for an empty/absent folder listing, so callers walking a tree must
// tolerate it rather than treat it as a failure.
var ErrNotFound = errors.New("not found")

// Config holds Vault connection and auth settings. Either Token (a pre-issued
// token) or RoleID+SecretID (AppRole) must be provided.
type Config struct {
	Addr        string // e.g. https://vault.example.com
	Namespace   string // Vault Enterprise / SecMan namespace; empty → root
	Token       string // pre-issued token; if set, AppRole login is skipped
	RoleID      string
	SecretID    string
	ApprolePath string // auth mount path; default "approle"
	KVMount     string // KV v2 mount; default "secret_v2"
	CACertPEM   []byte // optional CA bundle for the Vault TLS endpoint
	Insecure    bool   // skip TLS verification

	// Timeout caps a single Vault request; zero means defaultTimeout (30s) and a
	// negative value disables the cap. koc's --timeout feeds this.
	Timeout time.Duration

	Debug bool
}

// DefaultApprolePath and DefaultKVMount match the LCM deployment defaults.
const (
	DefaultApprolePath = "approle"
	DefaultKVMount     = "secret_v2"
)

// Client is a minimal Vault REST client.
type Client struct {
	cfg   Config
	hc    *http.Client
	token string
}

// New builds a client and, unless a token is supplied, performs an AppRole
// login so the returned client is ready to read secrets.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Addr == "" {
		return nil, fmt.Errorf("vault address is required (--vault-addr / VAULT_ADDR)")
	}
	if cfg.ApprolePath == "" {
		cfg.ApprolePath = DefaultApprolePath
	}
	if cfg.KVMount == "" {
		cfg.KVMount = DefaultKVMount
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.Insecure {
		tlsCfg.InsecureSkipVerify = true
		// This endpoint carries the Vault token and, on the --creds-from-vault path,
		// every OpenStack credential the openrc holds. Never disable verification
		// here silently.
		warnInsecure("Vault at " + strings.TrimRight(cfg.Addr, "/") + " (--insecure-vault)")
	} else if len(cfg.CACertPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CACertPEM) {
			return nil, fmt.Errorf("no certificates parsed from vault CA bundle")
		}
		tlsCfg.RootCAs = pool
	}
	warnCleartext("the Vault endpoint", cfg.Addr)

	timeout := cfg.Timeout
	switch {
	case timeout == 0:
		timeout = defaultTimeout
	case timeout < 0:
		timeout = 0 // explicitly uncapped
	}

	c := &Client{
		cfg:   cfg,
		token: cfg.Token,
		hc:    newHTTPClient(tlsCfg, timeout),
	}

	if c.token == "" {
		if cfg.RoleID == "" || cfg.SecretID == "" {
			return nil, fmt.Errorf("vault: provide a token (--vault-token) or an AppRole (--vault-role-id and --vault-secret-id)")
		}
		if err := c.approleLogin(ctx); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// approleLogin exchanges the AppRole role_id/secret_id for a client token.
func (c *Client) approleLogin(ctx context.Context) error {
	path := fmt.Sprintf("/v1/auth/%s/login", strings.Trim(c.cfg.ApprolePath, "/"))
	body, _ := json.Marshal(map[string]string{
		"role_id":   c.cfg.RoleID,
		"secret_id": c.cfg.SecretID,
	})
	var resp struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return fmt.Errorf("vault AppRole login: %w", err)
	}
	if resp.Auth.ClientToken == "" {
		return fmt.Errorf("vault AppRole login: no client token returned")
	}
	c.token = resp.Auth.ClientToken
	return nil
}

// Addr returns the Vault address the client talks to, without a trailing slash.
// Together with Namespace and KVMount it lets a caller report where it wrote and
// detect a source and destination that are in fact the same place.
func (c *Client) Addr() string { return strings.TrimRight(c.cfg.Addr, "/") }

// Namespace returns the Vault Enterprise namespace, empty for root.
func (c *Client) Namespace() string { return c.cfg.Namespace }

// KVMount returns the configured KV v2 mount, without surrounding slashes.
func (c *Client) KVMount() string { return strings.Trim(c.cfg.KVMount, "/") }

// ReadKVData reads a KV v2 secret from the client's configured mount and returns
// its data map (the inner "data.data" of the KV v2 response). path is the secret
// path within the mount, without the mount or the "data/" infix.
func (c *Client) ReadKVData(ctx context.Context, path string) (map[string]any, error) {
	return c.ReadKVDataAt(ctx, c.cfg.KVMount, path, 0)
}

// ReadKVDataAt reads a KV v2 secret from an explicit mount. version 0 reads the
// latest version.
func (c *Client) ReadKVDataAt(ctx context.Context, mount, path string, version int) (map[string]any, error) {
	full := kvPath(mount, "data", path)
	if version > 0 {
		full += "?version=" + url.QueryEscape(fmt.Sprint(version))
	}
	var resp struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, full, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Data.Data == nil {
		return nil, fmt.Errorf("vault: secret %q has no data", path)
	}
	return resp.Data.Data, nil
}

// WriteKVData creates or updates a KV v2 secret (writing a new version). Only
// the secret data is written — custom_metadata and version history are not
// touched.
func (c *Client) WriteKVData(ctx context.Context, mount, path string, data map[string]any) error {
	body, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		return fmt.Errorf("encoding secret %q: %w", path, err)
	}
	return c.do(ctx, http.MethodPost, kvPath(mount, "data", path), body, nil)
}

// ListKV lists the immediate children of a KV v2 path. Folder entries keep their
// trailing "/" (Vault's own convention), which is how a caller tells a subtree
// from a leaf secret. A missing or empty path yields ErrNotFound.
func (c *Client) ListKV(ctx context.Context, mount, path string) ([]string, error) {
	var resp struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	// Vault's LIST verb is equivalent to GET with ?list=true, which avoids
	// depending on the non-standard method.
	if err := c.do(ctx, http.MethodGet, kvPath(mount, "metadata", path)+"?list=true", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data.Keys, nil
}

// HasKV reports whether a KV v2 secret exists (its metadata is readable).
func (c *Client) HasKV(ctx context.Context, mount, path string) (bool, error) {
	err := c.do(ctx, http.MethodGet, kvPath(mount, "metadata", path), nil, nil)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// WalkKV returns every leaf secret under root, as paths relative to root, sorted
// for a deterministic copy order. An empty or absent subfolder is skipped rather
// than failing the whole walk. A root that is itself a leaf secret (no listing)
// yields an empty slice — callers distinguish that case via ErrNotFound.
func (c *Client) WalkKV(ctx context.Context, mount, root string) ([]string, error) {
	var out []string
	var walk func(rel string) error
	walk = func(rel string) error {
		keys, err := c.ListKV(ctx, mount, joinKV(root, rel))
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		for _, k := range keys {
			if k == "" {
				continue
			}
			// The listing is data from the server, and callers join it onto a
			// destination path, so a key that could escape the subtree fails the walk
			// rather than being silently carried along.
			if err := ValidateRelPath(strings.TrimSuffix(k, "/")); err != nil {
				return fmt.Errorf("vault listing of %q returned an unusable key %q: %w", joinKV(root, rel), k, err)
			}
			if strings.HasSuffix(k, "/") {
				if err := walk(joinKV(rel, strings.TrimSuffix(k, "/"))); err != nil {
					return err
				}
				continue
			}
			out = append(out, joinKV(rel, k))
		}
		return nil
	}
	if err := walk(""); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// kvPath builds "/v1/<mount>/<api>/<path>" for the KV v2 data/metadata APIs.
//
// Every segment is escaped individually. Plain concatenation let a secret path
// steer the request instead of naming it: a "?" started a query string (so
// "x?list=true" turned a read into a listing) and a "../" segment normalised out
// of the mount entirely. Splitting on "/" keeps the path structure while
// url.PathEscape neutralises everything inside a segment.
func kvPath(mount, api, path string) string {
	p := "/v1/" + url.PathEscape(strings.Trim(mount, "/")) + "/" + api
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if seg == "" {
			continue
		}
		p += "/" + url.PathEscape(seg)
	}
	return p
}

// ValidateRelPath rejects a relative KV path that must not be joined onto a base
// path. It is the guard for paths koc did not choose itself: the keys "kv copy
// -r" and "kv export" join come from the SOURCE Vault's own LIST response, so a
// hostile or spoofed source answering "../../../prod/openrc" would otherwise
// steer a write outside the subtree the operator named — which guardSelfCopy
// cannot see, because it compares the paths before the join.
func ValidateRelPath(rel string) error {
	if rel == "" {
		return nil
	}
	if strings.HasPrefix(rel, "/") {
		return fmt.Errorf("must be relative, not %q", rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		switch {
		case seg == "" || seg == ".":
			return fmt.Errorf("empty path segment in %q", rel)
		case strings.Contains(seg, ".."):
			return fmt.Errorf("path segment %q traverses out of the subtree", seg)
		case strings.ContainsAny(seg, "?#"):
			return fmt.Errorf("path segment %q contains a URL metacharacter", seg)
		}
	}
	return nil
}

// joinKV joins two KV path segments, tolerating empty ones.
func joinKV(a, b string) string {
	a, b = strings.Trim(a, "/"), strings.Trim(b, "/")
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "/" + b
}

// ResolvePath maps a user-supplied KV path argument to the path used under the
// mount's data/ API. It accepts three forms:
//   - a leading KV-mount segment ("secret_v2/…", the Vault CLI form) — the mount
//     is stripped and the remainder treated as absolute.
//   - a leading "/" — an absolute path; the prefix is ignored.
//   - otherwise — relative: prepended with the prefix (unless already prefixed).
func ResolvePath(prefix, mount, arg string) string {
	prefix = strings.Trim(prefix, "/")
	mount = strings.Trim(mount, "/")

	absolute := strings.HasPrefix(arg, "/")
	p := strings.Trim(arg, "/")

	// A leading "<mount>/" (or exactly the mount) is the Vault "mount/path" form;
	// drop it and treat the rest as an absolute KV path.
	if mount != "" && (p == mount || strings.HasPrefix(p, mount+"/")) {
		p = strings.TrimLeft(strings.TrimPrefix(p, mount), "/")
		absolute = true
	}

	if absolute {
		return p
	}
	if prefix == "" || p == prefix || strings.HasPrefix(p, prefix+"/") {
		return p
	}
	return prefix + "/" + p
}

// do performs a Vault API call. It sets the token and namespace headers and
// never dumps bodies; with debug on it logs only method, path and status. That
// is load-bearing, not stylistic: request and response bodies here carry Vault
// tokens and raw secret data, so --debug must never be able to leak them.
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.cfg.Addr, "/")+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("X-Vault-Token", c.token)
	}
	if c.cfg.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.cfg.Namespace)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if c.cfg.Debug {
		fmt.Fprintf(os.Stderr, "vault: %s %s -> %d\n", method, path, resp.StatusCode)
	}
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("vault %s %s: %w", method, path, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("vault %s %s: %s: %s", method, path, resp.Status, vaultError(payload))
	}
	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			return fmt.Errorf("decoding vault response: %w", err)
		}
	}
	return nil
}

// vaultError extracts the first message from a Vault {"errors":[...]} body.
func vaultError(body []byte) string {
	var e struct {
		Errors []string `json:"errors"`
	}
	if json.Unmarshal(body, &e) == nil && len(e.Errors) > 0 {
		return strings.Join(e.Errors, "; ")
	}
	if len(body) > 300 {
		body = body[:300]
	}
	return string(body)
}
