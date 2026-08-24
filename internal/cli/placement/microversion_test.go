package placement

import (
	"context"
	"net/http"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// --- parseMicroversion -------------------------------------------------------

func TestParseMicroversion(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantMajor int
		wantMinor int
	}{
		{"typical", "1.39", 1, 39},
		{"two digit major", "12.5", 12, 5},
		{"zero minor", "2.0", 2, 0},
		{"surrounding whitespace is trimmed", " 1.5 ", 1, 5},
		{"symbolic latest is unparseable", "latest", -1, -1},
		{"empty is unparseable", "", -1, -1},
		{"no dot at all", "139", -1, -1},
		{"non-numeric major", "a.5", -1, -1},
		{"non-numeric minor", "1.b", -1, -1},
		{"missing minor after the dot", "1.", -1, -1},
		{"missing major before the dot", ".5", -1, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotMajor, gotMinor := parseMicroversion(tc.in)
			if gotMajor != tc.wantMajor || gotMinor != tc.wantMinor {
				t.Errorf("parseMicroversion(%q) = (%d, %d), want (%d, %d)",
					tc.in, gotMajor, gotMinor, tc.wantMajor, tc.wantMinor)
			}
		})
	}
}

// --- compareMicroversions ----------------------------------------------------

func TestCompareMicroversions(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"equal", "1.39", "1.39", 0},
		// The real gotcha this function exists to avoid: a naive string compare
		// would say "1.39" < "1.5" (lexicographic, since '3' < '5'), which is
		// backwards from the numeric truth that 39 > 5.
		{"minor comparison is numeric, not lexicographic", "1.5", "1.39", -1},
		{"minor less", "1.5", "1.40", -1},
		{"minor greater", "1.40", "1.39", 1},
		{"major dominates minor", "2.0", "1.99", 1},
		{"major less regardless of minor", "1.99", "2.0", -1},
		{"an unparseable version sorts below a real one", "latest", "1.0", -1},
		{"a real version sorts above an unparseable one", "1.0", "latest", 1},
		{"two unparseable versions compare equal", "bad", "worse", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := compareMicroversions(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("compareMicroversions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// --- isNumericMicroversion ---------------------------------------------------

func TestIsNumericMicroversion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"symbolic latest", "latest", false},
		{"typical major.minor", "1.39", true},
		{"no dot", "139", false},
		{"two dots is rejected", "1.2.3", false},
		{"leading sign is rejected", "-1.5", false},
		{"trailing dot with no minor still counts as numeric", "1.", true},
		{"a lone dot has seen a dot and no other characters", ".", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNumericMicroversion(tc.in); got != tc.want {
				t.Errorf("isNumericMicroversion(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// --- placementMaxVersion -----------------------------------------------------

func TestPlacementMaxVersion_ParsesTheVersionDocument(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"versions": [{"min_version": "1.0", "max_version": "1.39"}]}`))
	})

	client := placementClient(fakeServer, "latest")
	got := placementMaxVersion(context.Background(), client)
	if got != "1.39" {
		t.Errorf("placementMaxVersion() = %q, want %q", got, "1.39")
	}
}

func TestPlacementMaxVersion_EmptyOnNon2xx(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	client := placementClient(fakeServer, "latest")
	if got := placementMaxVersion(context.Background(), client); got != "" {
		t.Errorf("placementMaxVersion() on a failed lookup = %q, want empty", got)
	}
}

func TestPlacementMaxVersion_EmptyWhenNoVersionsListed(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"versions": []}`))
	})

	client := placementClient(fakeServer, "latest")
	if got := placementMaxVersion(context.Background(), client); got != "" {
		t.Errorf("placementMaxVersion() with an empty versions list = %q, want empty", got)
	}
}

// --- concreteClient -----------------------------------------------------------

func TestConcreteClient_NumericMicroversionIsReturnedUntouchedWithNoRequest(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	called := false
	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	client := placementClient(fakeServer, "1.20")
	got := concreteClient(context.Background(), client)
	if got != client {
		t.Errorf("concreteClient returned a different client for an already-numeric microversion")
	}
	if called {
		t.Error("concreteClient made a version-document request although the microversion was already numeric")
	}
}

func TestConcreteClient_SymbolicMicroversionIsResolvedOnACopy(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"versions": [{"max_version": "1.39"}]}`))
	})

	client := placementClient(fakeServer, "latest")
	got := concreteClient(context.Background(), client)
	if got.Microversion != "1.39" {
		t.Errorf("concreteClient().Microversion = %q, want %q", got.Microversion, "1.39")
	}
	// The lookup must land on a shallow copy: setMicroversionHeader rewrites
	// MoreHeaders from client.Microversion on every request, so mutating the
	// caller's client here would leak "latest" -> "1.39" into later, unrelated
	// calls made against the shared ProviderClient.
	if client.Microversion != "latest" {
		t.Errorf("concreteClient mutated the caller's client: Microversion = %q, want %q",
			client.Microversion, "latest")
	}
	if got == client {
		t.Error("concreteClient returned the caller's client instead of a copy")
	}
}

func TestConcreteClient_FailedLookupReturnsTheClientUnchanged(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	client := placementClient(fakeServer, "latest")
	got := concreteClient(context.Background(), client)
	// A version-document hiccup must not replace the caller's client (and the
	// real error the caller's next request is about to hit) with a worse one.
	if got != client {
		t.Error("concreteClient replaced the client although the version lookup failed")
	}
	if got.Microversion != "latest" {
		t.Errorf("concreteClient.Microversion = %q, want %q unchanged", got.Microversion, "latest")
	}
}
