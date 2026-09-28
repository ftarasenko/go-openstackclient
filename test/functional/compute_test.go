//go:build functional

package functional

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"slices"
	"strings"
	"testing"
)

// The nova suite, minus servers themselves (server_test.go): flavors, keypairs,
// aggregates, the compute services and hosts, hypervisors, and the read-only
// accounting verbs.

// anyField is the first non-empty of several spellings of a field.
func anyField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := field(m, k); v != "" {
			return v
		}
	}
	return ""
}

// computeHost is the name of devstack's one compute host.
func computeHost(t *testing.T, r runner) string {
	t.Helper()
	for _, s := range r.list(t, "compute", "service", "list", "--service", "nova-compute") {
		if h := field(s, "host"); h != "" {
			return h
		}
	}
	t.Fatal("no nova-compute service")
	return ""
}

// sshPublicKey returns a fresh ed25519 key in authorized_keys form. nova stopped
// generating keypairs at 2.92, so a keypair needs a public key handed to it.
func sshPublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wire := func(b []byte) []byte {
		out := binary.BigEndian.AppendUint32(nil, uint32(len(b)))
		return append(out, b...)
	}
	blob := append(wire([]byte("ssh-ed25519")), wire(pub)...)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " koc-functional"
}

func TestComputeFlavors(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)

	name := uniq("flavor")
	f := r.show(t, "flavor", "create", name, "--ram", "128", "--disk", "1", "--vcpus", "1",
		"--private", "--property", "ft:one=1", "--description", "functional")
	t.Cleanup(func() { r.run(t, "flavor", "delete", name) })
	if field(f, "name") != name || !strings.Contains(anyField(f, "properties", "extra_specs"), "ft:one") {
		t.Fatalf("flavor create = %v", f)
	}

	r.ok(t, "flavor", "set", name, "--property", "ft:two=2", "--project", "demo")
	r.ok(t, "flavor", "unset", name, "--property", "ft:one")
	f = r.show(t, "flavor", "show", name)
	props := anyField(f, "properties", "extra_specs")
	if !strings.Contains(props, "ft:two") || strings.Contains(props, "ft:one") {
		t.Errorf("after flavor set/unset: %v", f)
	}
	r.ok(t, "flavor", "unset", name, "--project", "demo")
	if !slices.Contains(column(r.list(t, "flavor", "list", "--all"), "name"), name) {
		t.Errorf("flavor list --all does not list %s", name)
	}
}

func TestComputeKeypairs(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)

	name, pub := uniq("key"), sshPublicKey(t)
	file := writeFile(t, t.TempDir(), "id.pub", pub+"\n")
	r.ok(t, "keypair", "create", name, "--public-key", file)
	t.Cleanup(func() { r.run(t, "keypair", "delete", name) })

	k := r.show(t, "keypair", "show", name)
	if field(k, "name") != name || anyField(k, "fingerprint") == "" {
		t.Errorf("keypair show = %v", k)
	}
	if got := strings.TrimSpace(r.ok(t, "keypair", "show", name, "--public-key")); got != pub {
		t.Errorf("keypair show --public-key = %q, want %q", got, pub)
	}
	if !slices.Contains(column(r.list(t, "keypair", "list"), "name"), name) {
		t.Errorf("keypair list does not list %s", name)
	}
	r.ok(t, "keypair", "delete", name)
	r.fails(t, "keypair", "show", name)
}

func TestComputeAggregates(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)
	host := computeHost(t, r)

	name := uniq("agg")
	a := r.show(t, "aggregate", "create", name, "--zone", "ft-zone", "--property", "ft=1")
	t.Cleanup(func() {
		r.run(t, "aggregate", "remove", "host", name, host)
		r.run(t, "aggregate", "delete", name)
	})
	if field(a, "availability zone") != "ft-zone" {
		t.Fatalf("aggregate create = %v", a)
	}
	r.ok(t, "aggregate", "set", name, "--property", "ft2=2")
	r.ok(t, "aggregate", "unset", name, "--property", "ft")
	r.ok(t, "aggregate", "add", "host", name, host)
	a = r.show(t, "aggregate", "show", name)
	if !strings.Contains(field(a, "hosts"), host) || !strings.Contains(field(a, "properties"), "ft2") ||
		strings.Contains(field(a, "properties"), "ft=") {
		t.Errorf("after set/unset/add host: %v", a)
	}
	if !slices.Contains(column(r.list(t, "aggregate", "list", "--long"), "name"), name) {
		t.Errorf("aggregate list does not list %s", name)
	}
	r.ok(t, "aggregate", "remove", "host", name, host)
	if a = r.show(t, "aggregate", "show", name); strings.Contains(field(a, "hosts"), host) {
		t.Errorf("after remove host: %v", a)
	}
}

func TestComputeServicesAndHosts(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)
	host := computeHost(t, r)

	// Disable and re-enable the one nova-compute, with a reason.
	r.ok(t, "compute", "service", "set", host, "nova-compute", "--disable", "--disable-reason", "koc functional")
	t.Cleanup(func() { r.run(t, "compute", "service", "set", host, "nova-compute", "--enable") })
	svc := r.list(t, "compute", "service", "list", "--host", host, "--service", "nova-compute", "--long")
	if len(svc) != 1 || field(svc[0], "status") != "disabled" {
		t.Errorf("after --disable: %v", svc)
	}
	r.ok(t, "compute", "service", "set", host, "nova-compute", "--enable")
	// Deleting the real service would take the node out of the cloud; deleting
	// one that does not exist must fail cleanly.
	r.fails(t, "compute", "service", "delete", "ft-no-such-host", "nova-compute")

	// drain plans the host's servers without moving them (serial tests run
	// before the parallel server suites, so there are none yet); evacuate
	// refuses a host whose service is up and points at drain instead.
	if drain := r.ok(t, "compute", "host", "drain", host, "--dry-run"); !strings.Contains(drain, "ID") {
		t.Errorf("compute host drain --dry-run printed no plan: %s", drain)
	}
	if msg := r.fails(t, "compute", "host", "evacuate", host, "--dry-run"); !strings.Contains(msg, "drain") {
		t.Errorf("compute host evacuate of an up host: %q, want a pointer to drain", msg)
	}

	hv := r.list(t, "hypervisor", "list")
	if len(hv) == 0 {
		t.Fatal("hypervisor list is empty")
	}
	h := r.show(t, "hypervisor", "show", field(hv[0], "id"))
	if anyField(h, "hypervisor hostname", "hypervisor_hostname") == "" {
		t.Errorf("hypervisor show = %v", h)
	}
	// show names the hypervisor as list does (a UUID from nova 2.53), and still
	// carries the usage fields nova dropped at 2.88.
	if field(h, "id") != field(hv[0], "id") || field(h, "vcpus") == "" || field(h, "vcpus") == "0" {
		t.Errorf("hypervisor show %s: id %q, vcpus %q; want the listed id and nova's vcpus",
			field(hv[0], "id"), field(h, "id"), field(h, "vcpus"))
	}
	if byName := r.show(t, "hypervisor", "show", anyField(h, "hypervisor hostname", "hypervisor_hostname")); field(byName, "id") != field(hv[0], "id") {
		t.Errorf("hypervisor show by hostname: id %q, want %q", field(byName, "id"), field(hv[0], "id"))
	}

	zones := r.list(t, "availability", "zone", "list", "--long")
	if !in(zones, "nova") {
		t.Errorf("availability zone list = %v, want nova", zones)
	}
	if out := r.ok(t, "limits", "show", "--absolute", "-f", "json"); !strings.Contains(out, "max") {
		t.Errorf("limits show --absolute = %s, want the max* limits", out)
	}
	r.ok(t, "usage", "list")
	r.ok(t, "usage", "show", "--project", "admin")
}
