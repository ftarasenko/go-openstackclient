//go:build functional

package functional

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ironic is stacked API-only (up.sh's "ironic" feature): fake-hardware nodes
// with no VMs behind them, so every test here drives the API and its state
// machine, never a real deploy.

var (
	ironicOnce sync.Once
	ironicMax  string
	errIronic  error
)

// ironicAPI is the cloud's maximum ironic microversion, read from the
// endpoint's unauthenticated version document — the capability ironic
// advertises, so a test gates on it rather than on the series.
func ironicAPI(t *testing.T) string {
	t.Helper()
	ironicOnce.Do(func() {
		var url string
		for _, e := range defaultRunner(ft(t)).list(t, "endpoint", "list", "--interface", "public") {
			if field(e, "Service Type") == "baremetal" {
				url = field(e, "URL")
			}
		}
		if url == "" {
			errIronic = errNoIronic
			return
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			errIronic = err
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errIronic = err
			return
		}
		defer func() { _ = resp.Body.Close() }()
		var doc struct {
			Default struct {
				Version string `json:"version"`
			} `json:"default_version"`
		}
		errIronic = json.NewDecoder(resp.Body).Decode(&doc)
		ironicMax = doc.Default.Version
	})
	if errIronic != nil {
		t.Fatalf("reading the ironic version document: %v", errIronic)
	}
	return ironicMax
}

var errNoIronic = errors.New("no public baremetal endpoint in the catalog")

// requireIronicAPI skips unless the cloud's ironic speaks at least minimum.
func requireIronicAPI(t *testing.T, minimum string) {
	t.Helper()
	requireFeature(t, "ironic")
	if have := ironicAPI(t); !microversionAtLeast(have, minimum) {
		t.Skipf("needs ironic API %s; this cloud's ironic speaks up to %s", minimum, have)
	}
}

// microversionAtLeast compares two "major.minor" versions.
func microversionAtLeast(have, want string) bool {
	parse := func(v string) (int, int) {
		maj, mnr, _ := strings.Cut(v, ".")
		a, _ := strconv.Atoi(maj)
		b, _ := strconv.Atoi(mnr)
		return a, b
	}
	ha, hb := parse(have)
	wa, wb := parse(want)
	return ha > wa || ha == wa && hb >= wb
}

// TestIronicCapability pins the ironic API each cell stacks, as
// TestCapabilityMatrix does for neutron: a version-gated test turning into a
// skip must be a deliberate change, not a silent one. The floors are ironic's
// release mapping (ironic/common/release_mappings.py).
func TestIronicCapability(t *testing.T) {
	c := ft(t)
	requireIronicAPI(t, "1.1")
	floor := map[bool]string{false: "1.80", true: "1.96"}[c.Series.atLeast("2025.1")]
	if have := ironicAPI(t); !microversionAtLeast(have, floor) {
		t.Errorf("ironic on %s speaks up to %s, want at least %s", c.Series, have, floor)
	}
}

// newBareNode enrolls a fake-hardware node and deletes it when the test ends.
// It returns the node's name and UUID.
func newBareNode(t *testing.T, r runner) (name, uuid string) {
	t.Helper()
	name = uniq("bm")
	n := r.show(t, "baremetal", "node", "create", "--driver", "fake-hardware", "--name", name,
		"--resource-class", "CUSTOM_FT")
	uuid = field(n, "uuid")
	t.Cleanup(func() {
		// A node that left enroll must be back in a deletable state first;
		// maintenance makes delete skip the state check for an "available" one.
		r.run(t, "baremetal", "node", "maintenance", "set", uuid)
		r.run(t, "baremetal", "node", "delete", uuid)
	})
	return name, uuid
}

// items reads a JSON array member of a -f json object; nil when absent.
func items(m map[string]any, key string) []any {
	v, _ := m[key].([]any)
	return v
}

// object reads a JSON object member of a -f json object; nil when absent.
func object(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

func TestBaremetalNodeReads(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := defaultRunner(c)
	name, uuid := newBareNode(t, r)

	n := r.show(t, "baremetal", "node", "show", name)
	if field(n, "driver") != "fake-hardware" || field(n, "resource_class") != "CUSTOM_FT" {
		t.Errorf("node show = %v", n)
	}
	// The interfaces are filled from the hardware type's defaults; a blank one
	// means koc dropped the field, not that ironic left it out.
	for _, iface := range []string{"boot_interface", "deploy_interface", "management_interface", "power_interface", "network_interface"} {
		if field(n, iface) == "" {
			t.Errorf("node show has no %s: %v", iface, n)
		}
	}
	rows := r.list(t, "baremetal", "node", "list", "--long", "--resource-class", "CUSTOM_FT")
	i := slices.IndexFunc(rows, func(m map[string]any) bool { return field(m, "UUID") == uuid })
	if i < 0 {
		t.Fatalf("node list --long has no %s: %v", uuid, rows)
	}
	if field(rows[i], "Driver") != "fake-hardware" || field(rows[i], "Resource Class") != "CUSTOM_FT" ||
		field(rows[i], "Provisioning State") != "enroll" {
		t.Errorf("node list --long row = %v", rows[i])
	}
}

func TestBaremetalPortGroups(t *testing.T) {
	c := ft(t)
	requireFeature(t, "ironic")
	r := defaultRunner(c)
	node, nodeUUID := newBareNode(t, r)

	pgName := uniq("bond")
	pg := r.show(t, "baremetal", "port", "group", "create", "--node", node, "--name", pgName,
		"--address", "52:54:00:12:34:00", "--mode", "802.3ad", "--property", "miimon=100",
		"--unsupport-standalone-ports")
	pgUUID := field(pg, "uuid")
	t.Cleanup(func() { r.run(t, "baremetal", "port", "group", "delete", pgUUID) })
	if field(pg, "node_uuid") != nodeUUID || field(pg, "mode") != "802.3ad" || field(pg, "standalone_ports_supported") != "false" {
		t.Errorf("port group create = %v", pg)
	}

	for _, mac := range []string{"52:54:00:12:34:01", "52:54:00:12:34:02"} {
		p := r.show(t, "baremetal", "port", "create", mac, "--node", nodeUUID, "--port-group", pgUUID,
			"--physical-network", "physnet1")
		id := field(p, "uuid")
		t.Cleanup(func() { r.run(t, "baremetal", "port", "delete", id) })
	}

	if got := r.show(t, "baremetal", "port", "group", "show", pgName); len(items(got, "ports")) != 2 {
		t.Errorf("port group show ports = %v, want both members", got["ports"])
	}
	if got := r.show(t, "baremetal", "port", "group", "show", "--address", "52:54:00:12:34:00"); field(got, "name") != pgName {
		t.Errorf("port group show --address = %v", got)
	}
	rows := r.list(t, "baremetal", "port", "group", "list", "--node", node, "--long")
	if len(rows) != 1 || field(rows[0], "Mode") != "802.3ad" || len(items(rows[0], "Ports")) != 2 {
		t.Errorf("port group list --long = %v", rows)
	}
	if ports := r.list(t, "baremetal", "port", "list", "--port-group", pgName, "--long"); len(ports) != 2 ||
		field(ports[0], "Physical Network") != "physnet1" {
		t.Errorf("port list --port-group = %v", ports)
	}
	if ports := r.list(t, "baremetal", "port", "list", "--node", node, "--physical-network", "physnet2"); len(ports) != 0 {
		t.Errorf("port list --physical-network physnet2 = %v, want none", ports)
	}

	r.ok(t, "baremetal", "port", "group", "set", pgUUID, "--mode", "active-backup", "--extra", "rack=r1")
	got := r.show(t, "baremetal", "port", "group", "show", pgUUID)
	if field(got, "mode") != "active-backup" || field(object(got, "extra"), "rack") != "r1" {
		t.Errorf("after port group set: %v", got)
	}
	r.ok(t, "baremetal", "port", "group", "unset", pgUUID, "--extra", "rack", "--property", "miimon")
	got = r.show(t, "baremetal", "port", "group", "show", pgUUID)
	if len(object(got, "extra")) != 0 || len(object(got, "properties")) != 0 {
		t.Errorf("after port group unset: %v", got)
	}

	// A port group with members cannot be deleted; empty it first.
	for _, p := range r.list(t, "baremetal", "port", "list", "--port-group", pgUUID) {
		r.ok(t, "baremetal", "port", "delete", field(p, "UUID"))
	}
	r.ok(t, "baremetal", "port", "group", "delete", pgUUID)
	r.fails(t, "baremetal", "port", "group", "show", pgUUID)
}
