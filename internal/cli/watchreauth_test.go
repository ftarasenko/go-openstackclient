package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/watch"
)

// A watch runs for as long as an operator leaves it running, and a Keystone
// token does not: the default Fernet lifetime is an hour. Authenticating once
// per invocation used to hide that — every tick was a new process with a new
// token. Now one process holds one token across an unbounded loop, so what
// happens at expiry is koc's problem rather than the shell's.

// keystoneFleet is a mock Keystone plus nova whose tokens can be expired and
// whose re-authentication can be refused.
type keystoneFleet struct {
	mu       sync.Mutex
	minted   []string
	expired  map[string]bool
	refuse   bool // reject the next and every later authentication
	tokens   atomic.Int64
	lists    atomic.Int64
	base     string
	listCode func(n int64) int
}

func (k *keystoneFleet) handler(t *testing.T) http.Handler {
	t.Helper()
	k.expired = map[string]bool{}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v3/auth/tokens", func(w http.ResponseWriter, _ *http.Request) {
		n := k.tokens.Add(1)
		k.mu.Lock()
		refuse := k.refuse
		k.mu.Unlock()
		if refuse {
			// What Keystone answers a credential it no longer accepts.
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":401,"message":"The request you have made requires authentication."}}`))
			return
		}
		tok := "tok-" + string(rune('0'+n))
		k.mu.Lock()
		k.minted = append(k.minted, tok)
		k.mu.Unlock()

		w.Header().Set("X-Subject-Token", tok)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":{"methods":["password"],` +
			`"expires_at":"2099-01-01T00:00:00.000000Z",` +
			`"project":{"id":"p1","name":"proj","domain":{"id":"default","name":"Default"}},` +
			`"user":{"id":"u1","name":"alice","domain":{"id":"default","name":"Default"}},` +
			`"roles":[{"id":"r1","name":"admin"}],` +
			`"catalog":[{"type":"compute","name":"nova","id":"c1","endpoints":[` +
			`{"id":"e1","interface":"public","region":"RegionOne","region_id":"RegionOne",` +
			`"url":"` + k.base + `/v2.1"}]}]}}`))
	})

	mux.HandleFunc("GET /v2.1/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		n := k.lists.Add(1)
		tok := r.Header.Get("X-Auth-Token")
		k.mu.Lock()
		dead := k.expired[tok]
		k.mu.Unlock()
		if dead || (k.listCode != nil && k.listCode(n) == http.StatusUnauthorized) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"unauthorized":{"code":401,"message":"The request you have made requires authentication."}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[{"id":"11111111-1111-1111-1111-111111111111",` +
			`"name":"web-01","status":"ACTIVE","addresses":{},"flavor":{"original_name":"m1.small"}}]}`))
	})
	return mux
}

// expireAll marks every token minted so far as no longer accepted, which is
// what an operator's watch meets an hour in.
func (k *keystoneFleet) expireAll() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, tok := range k.minted {
		k.expired[tok] = true
	}
}

func (k *keystoneFleet) refuseAuth() {
	k.mu.Lock()
	k.refuse = true
	k.mu.Unlock()
}

func startFleet(t *testing.T, k *keystoneFleet) {
	t.Helper()
	srv := httptest.NewServer(k.handler(t))
	t.Cleanup(srv.Close)
	k.base = srv.URL

	t.Setenv("OS_AUTH_URL", srv.URL+"/v3")
	t.Setenv("OS_USERNAME", "alice")
	t.Setenv("OS_PASSWORD", "pw")
	t.Setenv("OS_PROJECT_NAME", "proj")
	t.Setenv("OS_USER_DOMAIN_NAME", "Default")
	t.Setenv("OS_PROJECT_DOMAIN_NAME", "Default")
	t.Setenv("OS_CLOUD", "")
	t.Setenv("NO_COLOR", "1")
}

func TestWatchSurvivesTokenExpiry(t *testing.T) {
	k := &keystoneFleet{}
	// The first refresh succeeds; the token is then dead, as it would be an
	// hour into a watch.
	k.listCode = func(int64) int { return 0 }
	startFleet(t, k)

	var once sync.Once
	k.listCode = func(n int64) int {
		if n == 1 {
			once.Do(k.expireAll)
		}
		return 0
	}

	out, err := runRoot(t, NewRootCommand("test"),
		"server", "list", "-c", "Name", "-c", "Status",
		"--watch="+watch.MinInterval.String(), "--watch-count=3")
	if err != nil {
		t.Fatalf("a watch must outlive its token: %v\n%s", err, out)
	}
	if n := strings.Count(out, "web-01"); n != 3 {
		t.Errorf("rendered %d frames, want 3:\n%s", n, out)
	}
	// One token to start, one more when the first expired — not one per tick,
	// which is the cost --watch exists to remove.
	if got := k.tokens.Load(); got != 2 {
		t.Errorf("minted %d tokens, want 2 (the original and its replacement)", got)
	}
}

func TestWatchStopsWhenReauthenticationIsRefused(t *testing.T) {
	k := &keystoneFleet{}
	startFleet(t, k)

	var once sync.Once
	k.listCode = func(n int64) int {
		if n == 1 {
			once.Do(func() {
				k.expireAll()
				// The credential itself is no longer accepted: the password
				// changed, the account was disabled, the application
				// credential was revoked.
				k.refuseAuth()
			})
		}
		return 0
	}

	out, err := runRoot(t, NewRootCommand("test"),
		"server", "list", "-c", "Name", "-c", "Status",
		"--watch="+watch.MinInterval.String(), "--watch-count=20")
	if err == nil {
		t.Fatalf("the watch kept going with a credential Keystone refuses:\n%s", out)
	}
	// Retrying a refused credential once a second is how an operator locks
	// their own account out while watching the screen that is doing it. Pinned
	// to the exact count rather than a bound: the original token, and the one
	// attempt Keystone refused. Anything more is a burst.
	if got := k.tokens.Load(); got != 2 {
		t.Errorf("attempted %d authentications, want exactly 2 — the original and the one refusal", got)
	}
	if got := k.lists.Load(); got != 2 {
		t.Errorf("issued %d list requests, want 2 — one good frame and the one that found the refusal", got)
	}
	// The other place a burst could come from is a tick that fans out —
	// `hypervisor list --placement` makes eight concurrent requests — but
	// gophercloud collapses concurrent re-authentications into one through the
	// reauthlock that openstack.NewClient installs (UseTokenLock), so eight
	// simultaneous 401s still cost one refused login rather than eight.
}
