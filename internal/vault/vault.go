// Package vault is a dependency-free, minimal HashiCorp Vault client. It exists
// so koc can fetch an openrc-style KV v2 secret and authenticate the normal
// Keystone flow from it, without vendoring the Vault SDK (honoring the repo's
// air-gap / minimal-dependency invariant). It supports AppRole login (or a
// pre-issued token), the KV v2 read API, and Vault Enterprise namespaces via the
// X-Vault-Namespace header.
package vault

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

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
	Debug       bool
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
	} else if len(cfg.CACertPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CACertPEM) {
			return nil, fmt.Errorf("no certificates parsed from vault CA bundle")
		}
		tlsCfg.RootCAs = pool
	}

	c := &Client{
		cfg:   cfg,
		token: cfg.Token,
		hc: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
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

// ReadKVData reads a KV v2 secret and returns its data map (the inner
// "data.data" of the KV v2 response). path is the secret path within the mount,
// without the mount or the "data/" infix.
func (c *Client) ReadKVData(ctx context.Context, path string) (map[string]any, error) {
	full := fmt.Sprintf("/v1/%s/data/%s", strings.Trim(c.cfg.KVMount, "/"), strings.TrimLeft(path, "/"))
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

// do performs a Vault API call. It sets the token and namespace headers and
// never dumps bodies (they carry tokens/secrets); with debug on it logs only
// method, path and status.
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
