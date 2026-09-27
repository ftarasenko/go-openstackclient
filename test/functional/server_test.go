//go:build functional

package functional

import (
	"strings"
	"testing"
	"time"
)

// The server suite: a cirros instance driven through every state nova offers a
// single node, plus its networking, volumes and server groups. What one node
// cannot do (a cold migration, an evacuation, a live migration to abort) is
// still invoked, and nova's refusal is the assertion: it proves koc sent a
// request nova understood.

const bootTimeout = 5 * time.Minute

// boot creates a server and waits for ACTIVE; the test deletes it at the end.
func boot(t *testing.T, r runner, c *cloud, name string, extra ...string) string {
	t.Helper()
	s := r.show(t, append([]string{"server", "create", name, "--image", c.Image, "--flavor", "m1.tiny",
		"--network", "private", "--wait"}, extra...)...)
	id := field(s, "id")
	if id == "" {
		t.Fatalf("server create returned %v", s)
	}
	t.Cleanup(func() {
		r.run(t, "server", "unlock", id)
		r.run(t, "server", "delete", id, "--wait")
	})
	return id
}

// status is the server's status and, while nova works on it, its task state.
func status(t *testing.T, r runner, id string) (string, string) {
	t.Helper()
	s := r.show(t, "server", "show", id)
	return field(s, "status"), anyField(s, "OS-EXT-STS:task_state", "task_state")
}

// settle waits until the server reaches want with no task in flight.
func settle(t *testing.T, r runner, id, want string) {
	t.Helper()
	waitFor(t, "server "+id+" to settle in "+want, bootTimeout, func() bool {
		st, task := status(t, r, id)
		if st == "ERROR" {
			t.Fatalf("server %s went to ERROR waiting for %s", id, want)
		}
		return st == want && (task == "" || task == "None")
	})
}

func TestServerLifecycle(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	t.Parallel()
	r := defaultRunner(c)

	key := uniq("key")
	r.ok(t, "keypair", "create", key, "--public-key", writeFile(t, t.TempDir(), "id.pub", sshPublicKey(t)))
	t.Cleanup(func() { r.run(t, "keypair", "delete", key) })
	userData := writeFile(t, t.TempDir(), "user-data", "#cloud-config\nhostname: ft\n")

	name := uniq("srv")
	id := boot(t, r, c, name, "--key-name", key, "--property", "ft=1", "--security-group", "default",
		"--user-data", userData, "--config-drive")

	s := r.show(t, "server", "show", id)
	if field(s, "key_name") != key || !strings.Contains(field(s, "properties"), "ft") {
		t.Errorf("server show = %v", s)
	}
	// --user-data writes the decoded script raw, for a pipe, whatever -f says.
	if ud := r.ok(t, "server", "show", id, "--user-data"); ud != "#cloud-config\nhostname: ft\n" {
		t.Errorf("server show --user-data = %q", ud)
	}

	// Properties, tags, name and description.
	r.ok(t, "server", "set", id, "--name", name+"-renamed", "--description", "functional",
		"--property", "ft2=2", "--tag", "ft-tag")
	s = r.show(t, "server", "show", id)
	if field(s, "name") != name+"-renamed" || field(s, "description") != "functional" ||
		!strings.Contains(field(s, "tags"), "ft-tag") {
		t.Errorf("after server set: %v", s)
	}
	r.ok(t, "server", "unset", id, "--property", "ft2", "--tag", "ft-tag")
	if s = r.show(t, "server", "show", id); strings.Contains(field(s, "properties"), "ft2") || strings.Contains(field(s, "tags"), "ft-tag") {
		t.Errorf("after server unset: %v", s)
	}

	// Power and pause states, each round trip back to ACTIVE. Spelled out
	// rather than looped: TestEveryLeafIsCovered reads literal command words.
	r.ok(t, "server", "stop", id)
	settle(t, r, id, "SHUTOFF")
	r.ok(t, "server", "start", id)
	settle(t, r, id, "ACTIVE")
	r.ok(t, "server", "pause", id)
	settle(t, r, id, "PAUSED")
	r.ok(t, "server", "unpause", id)
	settle(t, r, id, "ACTIVE")
	r.ok(t, "server", "suspend", id)
	settle(t, r, id, "SUSPENDED")
	r.ok(t, "server", "resume", id)
	settle(t, r, id, "ACTIVE")
	r.ok(t, "server", "rescue", id)
	settle(t, r, id, "RESCUE")
	r.ok(t, "server", "unrescue", id)
	settle(t, r, id, "ACTIVE")
	r.ok(t, "server", "reboot", id, "--soft")
	settle(t, r, id, "ACTIVE")
	r.ok(t, "server", "reboot", id, "--hard")
	settle(t, r, id, "ACTIVE")

	// A locked server refuses a state change until unlocked.
	r.ok(t, "server", "lock", id)
	if s = r.show(t, "server", "show", id); field(s, "locked") != "true" {
		t.Errorf("after lock: locked = %q", field(s, "locked"))
	}
	r.ok(t, "server", "unlock", id)

	// The console and what the guest wrote to it.
	waitFor(t, "console output", bootTimeout, func() bool {
		return strings.TrimSpace(r.ok(t, "console", "log", "show", id, "--lines", "20")) != ""
	})
	r.ok(t, "server", "console", "log", "show", id, "--lines", "5")
	// A console type devstack did not enable is nova's 400, not koc's failure.
	consoleOK := func(res result) {
		t.Helper()
		if res.code != 0 && !strings.Contains(strings.ToLower(res.stderr), "console") {
			t.Errorf("console url show: exit %d: %s", res.code, res.stderr)
		}
	}
	consoleOK(r.run(t, "console", "url", "show", id, "--novnc"))
	consoleOK(r.run(t, "server", "console", "url", "show", id, "--novnc"))
	// cirros posts no admin password, which koc reports rather than printing
	// an empty one.
	if msg := r.fails(t, "server", "password", "show", id); !strings.Contains(msg, "no stored admin password") {
		t.Errorf("server password show on a guest that posted none: %q", msg)
	}

	// Every action above is an instance action.
	events := r.list(t, "server", "event", "list", id)
	if !in(events, "create") || !in(events, "stop") {
		t.Errorf("server event list = %v, want create and stop", events)
	}
	ev := r.show(t, "server", "event", "show", id, anyField(events[0], "request id", "request_id"))
	if anyField(ev, "action") == "" {
		t.Errorf("server event show = %v", ev)
	}

	// Snapshot, then rebuild from the image it made.
	snap := uniq("snap")
	r.ok(t, "server", "image", "create", id, "--name", snap, "--wait")
	t.Cleanup(func() { r.run(t, "image", "delete", snap) })
	r.ok(t, "server", "rebuild", id, "--image", c.Image)
	settle(t, r, id, "ACTIVE")

	// Resize up and confirm, then again and revert (devstack allows a resize to
	// the same host). The second target keeps m1.small's disk: libvirt refuses
	// to shrink one, and rolls the resize back instead of reaching VERIFY_RESIZE.
	r.ok(t, "server", "resize", id, "--flavor", "m1.small")
	settle(t, r, id, "VERIFY_RESIZE")
	r.ok(t, "server", "resize", id, "--confirm")
	settle(t, r, id, "ACTIVE")
	if f := field(r.show(t, "server", "show", id), "flavor"); !strings.Contains(f, "m1.small") {
		t.Errorf("flavor after resize --confirm = %q", f)
	}
	small := r.show(t, "flavor", "show", "m1.small")
	flavor := uniq("resize")
	r.ok(t, "flavor", "create", flavor, "--ram", "256", "--vcpus", "1", "--disk", field(small, "disk"))
	t.Cleanup(func() { r.run(t, "flavor", "delete", flavor) })
	r.ok(t, "server", "resize", id, "--flavor", flavor)
	settle(t, r, id, "VERIFY_RESIZE")
	r.ok(t, "server", "resize", id, "--revert")
	settle(t, r, id, "ACTIVE")
	if f := field(r.show(t, "server", "show", id), "flavor"); !strings.Contains(f, "m1.small") {
		t.Errorf("flavor after resize --revert = %q, want m1.small back", f)
	}

	// The resizes are migrations. The per-server migration views and actions
	// only apply to a live migration in progress, which one node never has.
	migs := r.list(t, "server", "migration", "list", "--server", id)
	if len(migs) == 0 {
		t.Fatalf("server migration list --server %s is empty after two resizes", id)
	}
	mig := anyField(migs[0], "id")
	r.fails(t, "server", "migration", "show", id, mig)
	r.fails(t, "server", "migration", "abort", id, mig)
	r.fails(t, "server", "migration", "force", "complete", id, mig)

	// Cold migration needs a second host and evacuation a dead one: nova refuses
	// both on this node, synchronously or by leaving the server where it was.
	if res := r.run(t, "server", "migrate", id); res.code == 0 {
		settle(t, r, id, "ACTIVE")
	}
	r.fails(t, "server", "evacuate", id)

	// Shelve (offloaded at once in devstack) and bring it back.
	r.ok(t, "server", "shelve", id, "--wait")
	r.ok(t, "server", "unshelve", id, "--wait")
	settle(t, r, id, "ACTIVE")

	r.ok(t, "server", "delete", id, "--wait")
	r.fails(t, "server", "show", id)
}

func TestServerNetworkingAndVolumes(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	t.Parallel()
	r := defaultRunner(c)
	id := boot(t, r, c, uniq("netsrv"))

	// A second network to attach and detach.
	net := uniq("net")
	r.ok(t, "network", "create", net)
	t.Cleanup(func() { r.run(t, "network", "delete", net) })
	r.ok(t, "subnet", "create", net+"-sub", "--network", net, "--subnet-range", "198.51.100.0/24")

	addrs := func() string { return field(r.show(t, "server", "show", id), "addresses") }

	r.ok(t, "server", "add", "network", id, net)
	waitFor(t, "an address on "+net, time.Minute, func() bool { return strings.Contains(addrs(), "198.51.100.") })
	r.ok(t, "server", "remove", "network", id, net)
	waitFor(t, "the address on "+net+" to go", time.Minute, func() bool { return !strings.Contains(addrs(), "198.51.100.") })

	r.ok(t, "server", "add", "fixed", "ip", id, net, "--fixed-ip-address", "198.51.100.20")
	waitFor(t, "fixed IP 198.51.100.20", time.Minute, func() bool { return strings.Contains(addrs(), "198.51.100.20") })
	r.ok(t, "server", "remove", "fixed", "ip", id, "198.51.100.20")
	waitFor(t, "fixed IP 198.51.100.20 to go", time.Minute, func() bool { return !strings.Contains(addrs(), "198.51.100.20") })

	port := uniq("port")
	p := r.show(t, "port", "create", port, "--network", net)
	t.Cleanup(func() { r.run(t, "port", "delete", port) })
	r.ok(t, "server", "add", "port", id, field(p, "id"))
	waitFor(t, "the port to attach", time.Minute, func() bool { return strings.Contains(addrs(), "198.51.100.") })
	r.ok(t, "server", "remove", "port", id, field(p, "id"))
	waitFor(t, "the port to detach", time.Minute, func() bool { return !strings.Contains(addrs(), "198.51.100.") })

	sg := uniq("sg")
	r.ok(t, "security", "group", "create", sg)
	t.Cleanup(func() { r.run(t, "security", "group", "delete", sg) })
	r.ok(t, "server", "add", "security", "group", id, sg)
	if !strings.Contains(field(r.show(t, "server", "show", id), "security_groups"), sg) {
		t.Errorf("security group %s not on the server after add", sg)
	}
	r.ok(t, "server", "remove", "security", "group", id, sg)

	fip := r.show(t, "floating", "ip", "create", "public")
	ip := anyField(fip, "floating_ip_address", "floating ip address", "name")
	t.Cleanup(func() { r.run(t, "floating", "ip", "delete", ip) })
	r.ok(t, "server", "add", "floating", "ip", id, ip)
	waitFor(t, "floating IP "+ip, time.Minute, func() bool { return strings.Contains(addrs(), ip) })
	r.ok(t, "server", "remove", "floating", "ip", id, ip)
	waitFor(t, "floating IP "+ip+" to go", time.Minute, func() bool { return !strings.Contains(addrs(), ip) })

	vol := uniq("vol")
	r.ok(t, "volume", "create", vol, "--size", "1", "--wait")
	t.Cleanup(func() { r.run(t, "volume", "delete", vol) })
	r.ok(t, "server", "add", "volume", id, vol)
	waitFor(t, "the volume to attach", 2*time.Minute, func() bool {
		return field(r.show(t, "volume", "show", vol), "status") == "in-use"
	})
	r.ok(t, "server", "remove", "volume", id, vol)
	waitFor(t, "the volume to detach", 2*time.Minute, func() bool {
		return field(r.show(t, "volume", "show", vol), "status") == "available"
	})
}

func TestServerGroups(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	t.Parallel()
	r := defaultRunner(c)

	grp := uniq("sgrp")
	g := r.show(t, "server", "group", "create", grp, "--policy", "anti-affinity")
	gid := field(g, "id")
	t.Cleanup(func() { r.run(t, "server", "group", "delete", gid) })
	if !strings.Contains(anyField(g, "policy", "policies"), "anti-affinity") {
		t.Errorf("server group create = %v", g)
	}
	if got := r.show(t, "server", "group", "show", gid); field(got, "name") != grp {
		t.Errorf("server group show = %v", got)
	}
	if !in(r.list(t, "server", "group", "list", "--long"), grp) {
		t.Errorf("server group list does not list %s", grp)
	}

	id := boot(t, r, c, uniq("grpsrv"), "--server-group", gid)
	if !strings.Contains(field(r.show(t, "server", "group", "show", gid), "members"), id) {
		t.Errorf("server %s is not a member of %s", id, grp)
	}

	// Adding to and removing from a group after boot is a KeyStack extension
	// (dynamic server groups); stock nova has no such API and must say so.
	r.fails(t, "server", "remove", "server-group", id)
	r.fails(t, "server", "add", "server-group", id, gid)
}
