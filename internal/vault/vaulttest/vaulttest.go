// Package vaulttest is a fake Vault server for koc's tests.
//
// It serves the slice of the Vault HTTP API that internal/vault speaks — AppRole
// login, the KV v2 data and metadata endpoints (read, versioned read, write,
// list, existence), token and policy checks, and namespaces — and it answers the
// way a real server does, down to the status code and the error body. That last
// part is the point: a hand-written handler that returns 200 for an empty
// listing, or 404 with the wrong body, tests koc against a Vault that does not
// exist.
//
// The reference is a probe of OpenBao 2.7.0 (`bao server -dev`), whose KV v2 and
// AppRole APIs are Vault's. Every case below was observed there, recorded in
// docs/verification/2026-09-27-vault-api-probe.md, and is pinned by this
// package's tests:
//
//	LIST (or GET ?list=true) of an empty, missing or leaf path  404 {"errors":[]}
//	LIST keys                                   folders keep a trailing "/"
//	GET data or metadata of a missing secret    404 {"errors":[]}
//	GET data ?version=N past the current one    404 {"errors":[]}
//	GET data whose latest version is deleted    404 with data:null + metadata.deletion_time
//	no or unknown X-Vault-Token                 403 {"errors":["permission denied"]}
//	token lacking the capability                403 {"errors":["1 error occurred:\n\t* permission denied\n\n"]}
//	AppRole login, wrong role_id/secret_id      400 {"errors":["invalid role or secret ID"]}
//	AppRole login, no role_id                   500 {"errors":["missing role_id"]}
//	unknown X-Vault-Namespace                   404 {"errors":["namespace not found"]}
//	unknown mount                               404 {"errors":["no handler for route \"<path>\". route entry not found."]}
//	KV v1 mount read through the v2 data/ API   404 {"errors":[]}
//
// It is a fake, not a Vault: no leases, no token TTLs, no check-and-set, and a
// policy is a set of capabilities over the whole server rather than per path.
// Those are not things koc relies on; add them here, from a fresh probe, the
// day it does.
package vaulttest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Policy is what a token may do. The root token may do everything.
type Policy struct {
	Read, List, Write bool
}

// ReadOnly lets a token read and list but not write.
var ReadOnly = Policy{Read: true, List: true}

// ReadWrite lets a token read, list and write.
var ReadWrite = Policy{Read: true, List: true, Write: true}

// Request is one request the server received, for assertions on what koc sent.
type Request struct {
	Method    string
	Path      string // decoded, including the /v1 prefix
	Query     string
	Namespace string
	HasToken  bool
}

// Server is a running fake Vault.
type Server struct {
	// URL is the base address to give koc as --vault-addr / VAULT_ADDR.
	URL string
	// RootToken is a token with every capability.
	RootToken string

	srv         *httptest.Server
	approlePath string

	mu         sync.Mutex
	namespaces map[string]bool
	mounts     map[string]int // mount name → KV version (1 or 2)
	secrets    map[string]*secret
	roles      map[string]appRole // role_id → role
	tokens     map[string]token
	requests   []Request
	next       int
}

type appRole struct {
	secretID string
	policy   Policy
}

type token struct {
	namespace string
	policy    Policy
	root      bool
}

type secret struct {
	versions []version // versions[i] is version i+1
	created  time.Time
}

type version struct {
	data    map[string]any
	created time.Time
	deleted time.Time
}

// Option configures a Server.
type Option func(*Server)

// WithKVv2Mount adds a KV v2 mount. Every server already has "secret_v2", koc's
// default --vault-kv-mount.
func WithKVv2Mount(name string) Option {
	return func(s *Server) { s.mounts[strings.Trim(name, "/")] = 2 }
}

// WithKVv1Mount adds a KV v1 mount, which the v2 data/metadata API cannot read.
func WithKVv1Mount(name string) Option {
	return func(s *Server) { s.mounts[strings.Trim(name, "/")] = 1 }
}

// WithNamespace adds a namespace next to the root one. Every mount exists in
// every namespace, each with its own secrets and tokens.
func WithNamespace(ns string) Option {
	return func(s *Server) { s.namespaces[strings.Trim(ns, "/")] = true }
}

// WithAppRolePath mounts AppRole somewhere other than "approle".
func WithAppRolePath(p string) Option {
	return func(s *Server) { s.approlePath = strings.Trim(p, "/") }
}

// New starts a fake Vault and stops it when the test ends.
func New(t testing.TB, opts ...Option) *Server {
	t.Helper()
	s := &Server{
		RootToken:   "root",
		approlePath: "approle",
		namespaces:  map[string]bool{"": true},
		mounts:      map[string]int{"secret_v2": 2},
		secrets:     map[string]*secret{},
		roles:       map[string]appRole{},
		tokens:      map[string]token{},
	}
	for _, o := range opts {
		o(s)
	}
	for ns := range s.namespaces {
		s.tokens[s.nsToken(ns, s.RootToken)] = token{namespace: ns, root: true}
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

// nsToken keys a token by its namespace: a token is valid only where it was
// issued.
func (s *Server) nsToken(ns, tok string) string { return ns + "\x00" + tok }

func key(ns, mount, path string) string {
	return ns + "\x00" + mount + "\x00" + strings.Trim(path, "/")
}

// Put writes a new version of a secret, bypassing auth, and returns its version
// number. It is how a test seeds the server.
func (s *Server) Put(namespace, mount, path string, data map[string]any) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.put(namespace, mount, path, data)
}

func (s *Server) put(ns, mount, path string, data map[string]any) int {
	k := key(ns, mount, path)
	sec := s.secrets[k]
	now := time.Now().UTC()
	if sec == nil {
		sec = &secret{created: now}
		s.secrets[k] = sec
	}
	sec.versions = append(sec.versions, version{data: data, created: now})
	return len(sec.versions)
}

// SoftDelete deletes a secret's latest version the way `DELETE .../data/<path>`
// does: the version stays in the metadata, marked with a deletion time.
func (s *Server) SoftDelete(namespace, mount, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sec := s.secrets[key(namespace, mount, path)]; sec != nil && len(sec.versions) > 0 {
		sec.versions[len(sec.versions)-1].deleted = time.Now().UTC()
	}
}

// AddAppRole registers an AppRole in the root namespace whose login yields a
// token with policy p.
func (s *Server) AddAppRole(roleID, secretID string, p Policy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[roleID] = appRole{secretID: secretID, policy: p}
}

// AddToken registers a pre-issued token in the root namespace.
func (s *Server) AddToken(tok string, p Policy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[s.nsToken("", tok)] = token{policy: p}
}

// Requests returns every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// --- the HTTP side -------------------------------------------------------------

// Error bodies, verbatim from the probe.
const (
	denied       = "permission denied"
	policyDenied = "1 error occurred:\n\t* permission denied\n\n"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErrors(w http.ResponseWriter, status int, msgs ...string) {
	if msgs == nil {
		msgs = []string{}
	}
	writeJSON(w, status, map[string]any{"errors": msgs})
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ns := strings.Trim(r.Header.Get("X-Vault-Namespace"), "/")
	s.requests = append(s.requests, Request{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		Namespace: ns, HasToken: r.Header.Get("X-Vault-Token") != "",
	})
	if !s.namespaces[ns] {
		writeErrors(w, http.StatusNotFound, "namespace not found")
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/v1/")
	if !ok {
		writeErrors(w, http.StatusNotFound)
		return
	}
	if rest == "auth/"+s.approlePath+"/login" && r.Method == http.MethodPost {
		s.login(w, r, ns)
		return
	}

	tok, ok := s.tokens[s.nsToken(ns, r.Header.Get("X-Vault-Token"))]
	if !ok || r.Header.Get("X-Vault-Token") == "" {
		writeErrors(w, http.StatusForbidden, denied)
		return
	}

	mount, sub, _ := strings.Cut(rest, "/")
	kv, ok := s.mounts[mount]
	if !ok {
		writeErrors(w, http.StatusNotFound, fmt.Sprintf("no handler for route %q. route entry not found.", rest))
		return
	}
	api, path, _ := strings.Cut(sub, "/")
	if kv == 1 || (api != "data" && api != "metadata") {
		// A v1 mount has no data/metadata API: the request names a key that
		// does not exist, which v1 answers exactly like a missing v2 secret.
		writeErrors(w, http.StatusNotFound)
		return
	}
	s.kv2(w, r, tok, key(ns, mount, path), api)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, ns string) {
	var body struct {
		RoleID   string `json:"role_id"`
		SecretID string `json:"secret_id"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	_ = json.Unmarshal(raw, &body)
	if body.RoleID == "" {
		writeErrors(w, http.StatusInternalServerError, "missing role_id")
		return
	}
	role, ok := s.roles[body.RoleID]
	if !ok || ns != "" || body.SecretID != role.secretID {
		writeErrors(w, http.StatusBadRequest, "invalid role or secret ID")
		return
	}
	s.next++
	tok := fmt.Sprintf("s.fake%04d", s.next)
	s.tokens[s.nsToken(ns, tok)] = token{namespace: ns, policy: role.policy}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": nil,
		"auth": map[string]any{
			"client_token":   tok,
			"accessor":       fmt.Sprintf("accessor%04d", s.next),
			"policies":       []string{"default"},
			"token_policies": []string{"default"},
			"lease_duration": 2764800,
			"renewable":      true,
			"token_type":     "service",
		},
	})
}

func (s *Server) kv2(w http.ResponseWriter, r *http.Request, tok token, k, api string) {
	listing := r.Method == "LIST" || (r.Method == http.MethodGet && r.URL.Query().Get("list") == "true")
	switch {
	case listing && api == "metadata":
		if !tok.root && !tok.policy.List {
			writeErrors(w, http.StatusForbidden, policyDenied)
			return
		}
		s.list(w, k)
	case r.Method == http.MethodGet && api == "metadata":
		if !tok.root && !tok.policy.Read {
			writeErrors(w, http.StatusForbidden, policyDenied)
			return
		}
		s.metadata(w, k)
	case r.Method == http.MethodGet && api == "data":
		if !tok.root && !tok.policy.Read {
			writeErrors(w, http.StatusForbidden, policyDenied)
			return
		}
		s.read(w, r, k)
	case (r.Method == http.MethodPost || r.Method == http.MethodPut) && api == "data":
		if !tok.root && !tok.policy.Write {
			writeErrors(w, http.StatusForbidden, policyDenied)
			return
		}
		s.write(w, r, k)
	default:
		writeErrors(w, http.StatusMethodNotAllowed)
	}
}

func versionMeta(v version, n int) map[string]any {
	deleted := ""
	if !v.deleted.IsZero() {
		deleted = v.deleted.Format(time.RFC3339Nano)
	}
	return map[string]any{
		"created_time":    v.created.Format(time.RFC3339Nano),
		"custom_metadata": nil,
		"deletion_time":   deleted,
		"destroyed":       false,
		"version":         n,
	}
}

func (s *Server) read(w http.ResponseWriter, r *http.Request, k string) {
	sec := s.secrets[k]
	if sec == nil || len(sec.versions) == 0 {
		writeErrors(w, http.StatusNotFound)
		return
	}
	n := len(sec.versions)
	if q := r.URL.Query().Get("version"); q != "" && q != "0" {
		v, err := strconv.Atoi(q)
		if err != nil || v < 1 || v > len(sec.versions) {
			writeErrors(w, http.StatusNotFound)
			return
		}
		n = v
	}
	v := sec.versions[n-1]
	if !v.deleted.IsZero() {
		// A deleted version is a 404 that still carries its metadata.
		writeJSON(w, http.StatusNotFound, map[string]any{"data": map[string]any{"data": nil, "metadata": versionMeta(v, n)}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"data": v.data, "metadata": versionMeta(v, n)}})
}

func (s *Server) write(w http.ResponseWriter, r *http.Request, k string) {
	var body struct {
		Data map[string]any `json:"data"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(raw, &body); err != nil || body.Data == nil {
		writeErrors(w, http.StatusBadRequest, "no data provided")
		return
	}
	ns, rest, _ := strings.Cut(k, "\x00")
	mount, path, _ := strings.Cut(rest, "\x00")
	n := s.put(ns, mount, path, body.Data)
	writeJSON(w, http.StatusOK, map[string]any{"data": versionMeta(s.secrets[k].versions[n-1], n)})
}

func (s *Server) metadata(w http.ResponseWriter, k string) {
	sec := s.secrets[k]
	if sec == nil {
		writeErrors(w, http.StatusNotFound)
		return
	}
	versions := map[string]any{}
	for i, v := range sec.versions {
		m := versionMeta(v, i+1)
		delete(m, "version")
		delete(m, "custom_metadata")
		versions[strconv.Itoa(i+1)] = m
	}
	last := sec.versions[len(sec.versions)-1]
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"cas_required":         false,
		"created_time":         sec.created.Format(time.RFC3339Nano),
		"current_version":      len(sec.versions),
		"custom_metadata":      nil,
		"delete_version_after": "0s",
		"max_versions":         0,
		"oldest_version":       0,
		"updated_time":         last.created.Format(time.RFC3339Nano),
		"versions":             versions,
	}})
}

// list returns the immediate children of k's path: leaf secrets by name,
// folders with a trailing "/". An empty result is a 404, as on a real server —
// and so is listing a leaf secret, which has no children.
func (s *Server) list(w http.ResponseWriter, k string) {
	ns, rest, _ := strings.Cut(k, "\x00")
	mount, dir, _ := strings.Cut(rest, "\x00")
	prefix := key(ns, mount, "")
	if dir != "" {
		prefix += dir + "/"
	}
	seen := map[string]bool{}
	for sk := range s.secrets {
		child, ok := strings.CutPrefix(sk, prefix)
		if !ok || child == "" {
			continue
		}
		if name, _, folder := strings.Cut(child, "/"); folder {
			seen[name+"/"] = true
		} else {
			seen[name] = true
		}
	}
	if len(seen) == 0 {
		writeErrors(w, http.StatusNotFound)
		return
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"keys": keys}})
}
