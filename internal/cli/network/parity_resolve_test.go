package network

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// resolveMissingID is a synthetic UUID no fixture defines.
const resolveMissingID = "0badc0de-0000-4000-8000-00000000dead"

// resolveTestUUID is the ID the unique-match fixtures resolve to.
const resolveTestUUID = "7e3a1c52-9b1d-4f7e-8a6c-2d4b5e6f7a8b"

// resolveCollection is the JSON key neutron wraps each collection this
// package's resolvers list in, keyed by the collection's URL path.
var resolveCollection = map[string]string{
	"/networks":        "networks",
	"/subnets":         "subnets",
	"/routers":         "routers",
	"/ports":           "ports",
	"/security-groups": "security_groups",
	"/address-scopes":  "address_scopes",
	"/address-groups":  "address_groups",
	"/segments":        "segments",
	"/qos/policies":    "policies",
	"/trunks":          "trunks",
	"/subnetpools":     "subnetpools",
	"/floatingips":     "floatingips",
}

// requestLog records every request the mock sees as "METHOD /path?query".
type requestLog struct {
	mu   sync.Mutex
	reqs []string
}

func (l *requestLog) add(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		line += "?" + r.URL.RawQuery
	}
	l.reqs = append(l.reqs, line)
}

func (l *requestLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.reqs...)
}

// serveResolveMock routes every request through one catch-all handler that
// logs it (with or without the /v2.0 prefix execNetwork's endpoint adds). A
// collection GET is answered with matches (as JSON objects, already
// rendered) under the collection's key; a DELETE is answered 204; anything
// else 404 — so a request the resolver should not have made shows up both in
// the log and as a failure.
func serveResolveMock(t *testing.T, fakeServer th.FakeServer, matches ...string) *requestLog {
	t.Helper()
	log := &requestLog{}
	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		key, isCollection := resolveCollection[strings.TrimPrefix(r.URL.Path, "/v2.0")]
		switch {
		case isCollection && r.Method == http.MethodGet:
			writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{%q:[%s]}`, key, strings.Join(matches, ",")))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return log
}

type resolverCase struct {
	name     string
	kind     string // as it appears in the resolver's error messages
	path     string // the collection the lookup lists
	filter   string // the lookup's query key
	resolver func(context.Context, *gophercloud.ServiceClient, string) (string, error)
}

// resolverCases is every neutron name→ID resolver in the package. They share
// one policy, so each is driven through the same four cases.
var resolverCases = []resolverCase{
	{"network", "network", "/networks", "name", resolveNetworkID},
	{"subnet", "subnet", "/subnets", "name", resolveSubnetID},
	{"router", "router", "/routers", "name", resolveRouterID},
	{"port", "port", "/ports", "name", resolvePortID},
	{"security group", "security group", "/security-groups", "name", resolveSecGroupID},
	{"address scope", "address scope", "/address-scopes", "name", resolveAddressScopeID},
	{"address group", "address group", "/address-groups", "name", resolveAddressGroupID},
	{"segment", "network segment", "/segments", "name", resolveSubnetSegmentID},
	{"qos policy", "QoS policy", "/qos/policies", "name", resolveQoSPolicyID},
	{"trunk", "network trunk", "/trunks", "name", resolveTrunkID},
	{"subnet pool", "subnet pool", "/subnetpools", "name", resolveSubnetPoolID},
	{"floating ip", "floating IP", "/floatingips", "floating_ip_address", resolveFloatingIPID},
}

// matchJSON renders one lookup match. The subnet pool decoder insists on the
// prefix lengths, so every match carries them; the other decoders ignore them.
func matchJSON(filter, value, id string) string {
	return fmt.Sprintf(`{"id":%q,%q:%q,"default_prefixlen":0,"min_prefixlen":0,"max_prefixlen":0}`, id, filter, value)
}

func TestResolvers_UUIDPassesThroughWithNoRequest(t *testing.T) {
	for _, tc := range resolverCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			log := serveResolveMock(t, fakeServer)

			got, err := tc.resolver(context.Background(), networkClient(fakeServer), resolveTestUUID)
			if err != nil {
				t.Fatalf("resolve %s: %v", resolveTestUUID, err)
			}
			if got != resolveTestUUID {
				t.Errorf("resolved ID = %q, want the UUID unchanged", got)
			}
			if reqs := log.all(); len(reqs) != 0 {
				t.Errorf("a UUID made requests %v, want none", reqs)
			}
		})
	}
}

func TestResolvers_UniqueMatchWins(t *testing.T) {
	for _, tc := range resolverCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			ref := "edge"
			if tc.filter == "floating_ip_address" {
				ref = "203.0.113.5"
			}
			log := serveResolveMock(t, fakeServer, matchJSON(tc.filter, ref, resolveTestUUID))

			got, err := tc.resolver(context.Background(), networkClient(fakeServer), ref)
			if err != nil {
				t.Fatalf("resolve %q: %v", ref, err)
			}
			if got != resolveTestUUID {
				t.Errorf("resolved ID = %q, want %q", got, resolveTestUUID)
			}
			want := []string{"GET " + tc.path + "?" + tc.filter + "=" + ref}
			if reqs := log.all(); !reflect.DeepEqual(reqs, want) {
				t.Errorf("requests = %v, want %v", reqs, want)
			}
		})
	}
}

func TestResolvers_AmbiguousNameIsAnError(t *testing.T) {
	for _, tc := range resolverCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			serveResolveMock(t, fakeServer,
				matchJSON(tc.filter, "dup", resolveTestUUID),
				matchJSON(tc.filter, "dup", resolveMissingID))

			_, err := tc.resolver(context.Background(), networkClient(fakeServer), "dup")
			want := fmt.Sprintf(`%s "dup" is ambiguous: 2 matches, use the ID`, tc.kind)
			if err == nil || err.Error() != want {
				t.Errorf("error = %v, want %q", err, want)
			}
		})
	}
}

// A reference that matches nothing is an error, as upstream's find_* ("No
// Network found for typo"); it is never sent on as if it were an ID.
func TestResolvers_ZeroMatchIsAnError(t *testing.T) {
	for _, tc := range resolverCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			log := serveResolveMock(t, fakeServer)

			got, err := tc.resolver(context.Background(), networkClient(fakeServer), "typo")
			want := fmt.Sprintf(`no %s found for "typo"`, tc.kind)
			if err == nil || err.Error() != want {
				t.Errorf("resolve typo = %q, %v; want error %q", got, err, want)
			}
			wantReqs := []string{"GET " + tc.path + "?" + tc.filter + "=typo"}
			if reqs := log.all(); !reflect.DeepEqual(reqs, wantReqs) {
				t.Errorf("requests = %v, want only the lookup %v", reqs, wantReqs)
			}
		})
	}
}

func TestPickID_Policy(t *testing.T) {
	ids := []string{"a", "b"}
	at := func(i int) string { return ids[i] }
	if got, err := pickID("x", 1, at, "network"); err != nil || got != "a" {
		t.Errorf("one match = %q, %v; want a", got, err)
	}
	if _, err := pickID("x", 0, at, "network"); err == nil || err.Error() != `no network found for "x"` {
		t.Errorf("zero matches error = %v", err)
	}
	if _, err := pickID("x", 2, at, "network"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("two matches error = %v", err)
	}
}

// --- the verbs: a typo stops at the lookup ------------------------------------

func TestRunNetworkDelete_TypoSendsNoDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	log := serveResolveMock(t, fakeServer)

	var out bytes.Buffer
	err := runNetworkDelete(context.Background(), networkClient(fakeServer), []string{"typo"}, &out)
	if err == nil || !strings.Contains(err.Error(), `no network found for "typo"`) {
		t.Fatalf("error = %v, want the zero-match error", err)
	}
	if want := []string{"GET /networks?name=typo"}; !reflect.DeepEqual(log.all(), want) {
		t.Errorf("requests = %v, want only %v (no DELETE)", log.all(), want)
	}
}

func TestRunNetworkDelete_UUIDSkipsTheLookup(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	log := serveResolveMock(t, fakeServer)

	var out bytes.Buffer
	if err := runNetworkDelete(context.Background(), networkClient(fakeServer), []string{resolveTestUUID}, &out); err != nil {
		t.Fatalf("runNetworkDelete: %v", err)
	}
	if want := []string{"DELETE /networks/" + resolveTestUUID}; !reflect.DeepEqual(log.all(), want) {
		t.Errorf("requests = %v, want %v", log.all(), want)
	}
}

func TestRunRouterShow_UnknownNameSendsNoGet(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	log := serveResolveMock(t, fakeServer)

	var out bytes.Buffer
	o := &output.Options{Format: output.FormatValue}
	err := runRouterShow(context.Background(), networkClient(fakeServer), o, "nosuch", &out)
	if err == nil || err.Error() != `no router found for "nosuch"` {
		t.Fatalf("error = %v, want the zero-match error", err)
	}
	if want := []string{"GET /routers?name=nosuch"}; !reflect.DeepEqual(log.all(), want) {
		t.Errorf("requests = %v, want only %v", log.all(), want)
	}
}

func TestRunFloatingIPDelete_UnknownAddressSendsNoDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	log := serveResolveMock(t, fakeServer)

	var out bytes.Buffer
	err := runFloatingIPDelete(context.Background(), networkClient(fakeServer), []string{"203.0.113.9"}, &out)
	if err == nil || !strings.Contains(err.Error(), `no floating IP found for "203.0.113.9"`) {
		t.Fatalf("error = %v, want the zero-match error", err)
	}
	if want := []string{"GET /floatingips?floating_ip_address=203.0.113.9"}; !reflect.DeepEqual(log.all(), want) {
		t.Errorf("requests = %v, want only %v (no DELETE)", log.all(), want)
	}
}

// Through cobra: the message is what the user sees on stderr.
func TestExec_NetworkDelete_TypoErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	log := serveResolveMock(t, fakeServer)

	_, err := execNetwork(t, fakeServer, "network", "delete", "typo")
	if err == nil || !strings.Contains(err.Error(), `no network found for "typo"`) {
		t.Fatalf("network delete typo: error = %v", err)
	}
	if want := []string{"GET /v2.0/networks?name=typo"}; !reflect.DeepEqual(log.all(), want) {
		t.Errorf("requests = %v, want only %v (no DELETE)", log.all(), want)
	}
}
