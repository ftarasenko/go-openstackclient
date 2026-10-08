//go:build functional

package functional

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// The cinder suite: a volume and everything hung off it — snapshots, backups
// (into swift), transfers, attachments, types and QoS — plus the services,
// pools and clusters an operator reads. devstack runs one LVM backend, so what
// needs a second one (a migration) is invoked and cinder's refusal asserted.
//
// None of these call t.Parallel: the service test disables a cinder service for
// a moment, and serial tests finish before any parallel one starts.

const volumeTimeout = 3 * time.Minute

// demoRunner authenticates as devstack's demo user in the demo project — the
// other side of a share or a transfer.
func demoRunner(c *cloud) runner {
	return runner{env: append(baseEnv(),
		"OS_AUTH_URL="+openrcValue(c, "OS_AUTH_URL"), "OS_REGION_NAME="+openrcValue(c, "OS_REGION_NAME"),
		"OS_USERNAME=demo", "OS_PASSWORD="+openrcValue(c, "OS_PASSWORD"), "OS_PROJECT_NAME=demo",
		"OS_USER_DOMAIN_ID=default", "OS_PROJECT_DOMAIN_ID=default")}
}

// newVolume creates a 1 GiB volume, waits for it, and deletes it at the end.
func newVolume(t *testing.T, r runner, extra ...string) string {
	t.Helper()
	v := r.show(t, append([]string{"volume", "create", uniq("vol"), "--size", "1", "--wait"}, extra...)...)
	id := field(v, "id")
	if id == "" || field(v, "status") != "available" {
		t.Fatalf("volume create = %v", v)
	}
	t.Cleanup(func() { r.run(t, "volume", "delete", id) })
	return id
}

// statusIs waits for a cinder object's show to report want. It fails fast on
// cinder's error states.
func statusIs(t *testing.T, r runner, want string, show ...string) {
	t.Helper()
	waitFor(t, strings.Join(show, " ")+" to be "+want, volumeTimeout, func() bool {
		st := field(r.show(t, show...), "status")
		if strings.HasPrefix(st, "error") && !strings.HasPrefix(want, "error") {
			t.Fatalf("%s went to %s waiting for %s", strings.Join(show, " "), st, want)
		}
		return st == want
	})
}

// gone waits until show fails: cinder deletes asynchronously, and a volume
// with a snapshot still being deleted cannot itself be deleted.
func gone(t *testing.T, r runner, show ...string) {
	t.Helper()
	waitFor(t, strings.Join(show, " ")+" to go", volumeTimeout, func() bool { return r.run(t, show...).code != 0 })
}

// lvmPool is devstack's one pool, host@backend#pool.
func lvmPool(t *testing.T, r runner) string {
	t.Helper()
	pools := r.list(t, "volume", "backend", "pool", "list")
	if len(pools) == 0 {
		t.Fatal("volume backend pool list is empty")
	}
	return field(pools[0], "name")
}

func TestVolumeLifecycle(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)
	id := newVolume(t, r, "--property", "ft=1", "--description", "functional")

	renamed := uniq("vol-renamed")
	r.ok(t, "volume", "set", id, "--name", renamed, "--property", "ft2=2")
	r.ok(t, "volume", "unset", id, "--property", "ft")
	v := r.show(t, "volume", "show", id)
	if field(v, "name") != renamed || !strings.Contains(field(v, "metadata"), "ft2") ||
		strings.Contains(field(v, "metadata"), "ft:1") {
		t.Errorf("after volume set/unset: %v", v)
	}
	// volume list --long carries the metadata as upstream's Properties.
	if rows := r.list(t, "volume", "list", "--long", "--name", renamed); len(rows) != 1 ||
		!strings.Contains(field(rows[0], "properties"), "ft2") || strings.Contains(field(rows[0], "properties"), "ft:1") {
		t.Errorf("volume list --long --name %s = %v", renamed, rows)
	}
	// --property filters server-side on the same metadata: a match keeps the
	// volume, a mismatched value drops it.
	if ids := column(r.list(t, "volume", "list", "--property", "ft2=2"), "id"); !slices.Contains(ids, id) {
		t.Errorf("volume list --property ft2=2 = %v, want %s in it", ids, id)
	}
	if ids := column(r.list(t, "volume", "list", "--property", "ft2=nope"), "id"); slices.Contains(ids, id) {
		t.Errorf("volume list --property ft2=nope = %v, want %s left out", ids, id)
	}

	r.ok(t, "volume", "extend", id, "2")
	statusIs(t, r, "available", "volume", "show", id)
	r.ok(t, "volume", "set", id, "--size", "3")
	statusIs(t, r, "available", "volume", "show", id)
	if s := field(r.show(t, "volume", "show", id), "size"); s != "3" {
		t.Errorf("size after extend and set --size = %s, want 3", s)
	}

	// An admin status reset only rewrites cinder's record.
	r.ok(t, "volume", "set", id, "--state", "error")
	statusIs(t, r, "error", "volume", "show", id)
	r.ok(t, "volume", "set", id, "--state", "available")

	// One backend: migrating to the volume's own host is refused up front.
	if msg := r.fails(t, "volume", "migrate", id, "--host", lvmPool(t, r)); msg == "" {
		t.Error("volume migrate to the current host: no error message")
	}
}

func TestVolumeSnapshots(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)
	vol := newVolume(t, r)

	name := uniq("snap")
	s := r.show(t, "volume", "snapshot", "create", name, "--volume", vol, "--description", "functional")
	sid := field(s, "id")
	t.Cleanup(func() { r.run(t, "volume", "snapshot", "delete", sid) })
	statusIs(t, r, "available", "volume", "snapshot", "show", sid)

	r.ok(t, "volume", "snapshot", "set", sid, "--name", name+"-renamed", "--property", "ft=1", "--property", "ft2=2")
	r.ok(t, "volume", "snapshot", "unset", sid, "--property", "ft")
	s = r.show(t, "volume", "snapshot", "show", sid)
	if field(s, "name") != name+"-renamed" || !strings.Contains(field(s, "metadata"), "ft2") ||
		strings.Contains(field(s, "metadata"), "ft:1") {
		t.Errorf("after snapshot set/unset: %v", s)
	}
	if rows := r.list(t, "volume", "snapshot", "list", "--volume", vol); len(rows) != 1 || field(rows[0], "id") != sid {
		t.Errorf("volume snapshot list --volume = %v", rows)
	}
	// --long: the source volume is its ID in json and its name in the table.
	long := r.list(t, "volume", "snapshot", "list", "--volume", vol, "--long")
	if len(long) != 1 || field(long[0], "volume") != vol || !strings.Contains(field(long[0], "properties"), "ft2") ||
		field(long[0], "created at") == "" {
		t.Errorf("volume snapshot list --long = %v", long)
	}
	volName := field(r.show(t, "volume", "show", vol), "name")
	if out := r.ok(t, "volume", "snapshot", "list", "--volume", vol, "--long", "-c", "Volume"); !strings.Contains(out, volName) {
		t.Errorf("volume snapshot list --long table does not name volume %s:\n%s", volName, out)
	}

	// A volume made from the snapshot, then the snapshot goes.
	clone := newVolume(t, r, "--snapshot", sid)
	if field(r.show(t, "volume", "show", clone), "snapshot_id") != sid {
		t.Errorf("volume from snapshot does not record it")
	}
	r.ok(t, "volume", "delete", clone)
	gone(t, r, "volume", "show", clone)
	r.ok(t, "volume", "snapshot", "delete", sid)
	gone(t, r, "volume", "snapshot", "show", sid)
}

// Backups go to swift (up.sh enables c-bak with the swift driver).
func TestVolumeBackups(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)
	vol := newVolume(t, r)

	name := uniq("bak")
	b := r.show(t, "volume", "backup", "create", vol, "--name", name, "--description", "functional")
	bid := field(b, "id")
	t.Cleanup(func() { r.run(t, "volume", "backup", "delete", bid) })
	statusIs(t, r, "available", "volume", "backup", "show", bid)

	inc := r.show(t, "volume", "backup", "create", vol, "--name", name+"-inc", "--incremental")
	incID := field(inc, "id")
	t.Cleanup(func() { r.run(t, "volume", "backup", "delete", incID) })
	statusIs(t, r, "available", "volume", "backup", "show", incID)
	if field(r.show(t, "volume", "backup", "show", incID), "is_incremental") != "true" {
		t.Errorf("backup create --incremental made a full backup")
	}

	r.ok(t, "volume", "backup", "set", bid, "--name", name+"-renamed", "--description", "changed",
		"--property", "ft=1", "--property", "ft2=2")
	r.ok(t, "volume", "backup", "unset", bid, "--property", "ft")
	if b = r.show(t, "volume", "backup", "show", bid); field(b, "name") != name+"-renamed" || field(b, "description") != "changed" {
		t.Errorf("after backup set: %v", b)
	}
	backups := r.list(t, "volume", "backup", "list", "--volume", vol)
	if ids := column(backups, "id"); !slices.Contains(ids, bid) || !slices.Contains(ids, incID) {
		t.Errorf("volume backup list --volume = %v, want %s and %s", ids, bid, incID)
	}
	// The listing reads cinder's detail view: the summary has no status or size.
	for _, row := range backups {
		if field(row, "id") == bid && (field(row, "status") != "available" || field(row, "size") != "1" ||
			field(row, "description") != "changed") {
			t.Errorf("volume backup list row for %s = %v", bid, row)
		}
		if field(row, "id") == incID && field(row, "incremental") != "true" {
			t.Errorf("volume backup list does not mark %s incremental: %v", incID, row)
		}
	}
	// --long: the source volume is its ID in json, the container is swift's.
	for _, row := range r.list(t, "volume", "backup", "list", "--volume", vol, "--long") {
		if field(row, "volume") != vol || field(row, "container") == "" {
			t.Errorf("volume backup list --long row = %v", row)
		}
	}

	// Restore into a new volume, which the restore creates.
	res := r.show(t, "volume", "backup", "restore", bid)
	restored := field(res, "volume_id")
	if restored == "" {
		t.Fatalf("volume backup restore = %v", res)
	}
	t.Cleanup(func() { r.run(t, "volume", "delete", restored) })
	statusIs(t, r, "available", "volume", "show", restored)
	statusIs(t, r, "available", "volume", "backup", "show", bid)

	// The incremental depends on the full one, so it goes first.
	r.ok(t, "volume", "backup", "delete", incID)
	gone(t, r, "volume", "backup", "show", incID)
	r.ok(t, "volume", "backup", "delete", bid)
	gone(t, r, "volume", "backup", "show", bid)
}

// A transfer moves a volume to another project: admin offers, demo accepts.
func TestVolumeTransfer(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r, demo := defaultRunner(c), demoRunner(c)

	// One offered and withdrawn.
	withdrawn := newVolume(t, r)
	w := r.show(t, "volume", "transfer", "request", "create", withdrawn, "--name", uniq("xfer"))
	r.ok(t, "volume", "transfer", "request", "delete", field(w, "id"))
	statusIs(t, r, "available", "volume", "show", withdrawn)

	vol := newVolume(t, r)
	x := r.show(t, "volume", "transfer", "request", "create", vol, "--name", uniq("xfer"))
	xid, key := field(x, "id"), field(x, "auth_key")
	t.Cleanup(func() { r.run(t, "volume", "transfer", "request", "delete", xid) })
	if key == "" {
		t.Fatalf("transfer request create printed no auth_key: %v", x)
	}
	if got := r.show(t, "volume", "transfer", "request", "show", xid); field(got, "volume_id") != vol {
		t.Errorf("transfer request show = %v", got)
	}
	if !slices.Contains(column(r.list(t, "volume", "transfer", "request", "list"), "id"), xid) {
		t.Errorf("transfer request list does not list %s", xid)
	}

	demo.fails(t, "volume", "transfer", "request", "accept", xid, "--auth-key", "wrong-key")
	demo.ok(t, "volume", "transfer", "request", "accept", xid, "--auth-key", key)
	t.Cleanup(func() { demo.run(t, "volume", "delete", vol) })
	if v := demo.show(t, "volume", "show", vol); field(v, "id") != vol {
		t.Errorf("demo cannot see the accepted volume: %v", v)
	}
	demo.ok(t, "volume", "delete", vol)
	gone(t, demo, "volume", "show", vol)
}

// An attachment record, without nova: cinder takes the instance UUID on trust,
// and the connector is a host that does not exist (lioadm just adds an ACL).
func TestVolumeAttachments(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)
	vol := newVolume(t, r)
	const instance = "8d1c3b0e-0b5a-4d2c-9f4e-5a6b7c8d9e0f"

	a := r.show(t, "volume", "attachment", "create", vol, instance, "--mode", "rw")
	aid := field(a, "id")
	t.Cleanup(func() { r.run(t, "volume", "attachment", "delete", aid) })
	if field(a, "status") != "reserved" {
		t.Errorf("attachment create = %v, want reserved", a)
	}
	r.ok(t, "volume", "attachment", "set", aid, "--host", "ft-host", "--ip", "192.0.2.50",
		"--initiator", "iqn.2026-09.com.example:ft", "--platform", "x86_64", "--os-type", "linux2")
	r.ok(t, "volume", "attachment", "complete", aid)
	if a = r.show(t, "volume", "attachment", "show", aid); field(a, "status") != "attached" || field(a, "instance") != instance {
		t.Errorf("after set and complete: %v", a)
	}
	statusIs(t, r, "in-use", "volume", "show", vol)
	if !slices.Contains(column(r.list(t, "volume", "attachment", "list", "--volume-id", vol), "id"), aid) {
		t.Errorf("attachment list --volume-id does not list %s", aid)
	}
	r.ok(t, "volume", "attachment", "delete", aid)
	statusIs(t, r, "available", "volume", "show", vol)
}

func TestVolumeTypesAndQoS(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)
	// Extra specs are scoped (ft:…): the capabilities filter skips a scoped
	// key, where an unscoped one must match a backend capability.
	pool := lvmPool(t, r)
	backend := pool[strings.Index(pool, "@")+1:]
	backend, _, _ = strings.Cut(backend, "#")

	name := uniq("vtype")
	ty := r.show(t, "volume", "type", "create", name, "--description", "functional",
		"--property", "volume_backend_name="+backend, "--property", "ft:one=1")
	tid := field(ty, "id")
	t.Cleanup(func() { r.run(t, "volume", "type", "delete", tid) })
	r.ok(t, "volume", "type", "set", tid, "--name", name+"-renamed", "--property", "ft:two=2")
	r.ok(t, "volume", "type", "unset", tid, "--property", "ft:one")
	ty = r.show(t, "volume", "type", "show", tid)
	if field(ty, "name") != name+"-renamed" || !strings.Contains(field(ty, "extra_specs"), "ft:two") ||
		strings.Contains(field(ty, "extra_specs"), "ft:one") {
		t.Errorf("after type set/unset: %v", ty)
	}
	if !slices.Contains(column(r.list(t, "volume", "type", "list"), "id"), tid) {
		t.Errorf("volume type list does not list %s", tid)
	}
	// --long: the extra specs, as upstream's Properties.
	var listed bool
	for _, row := range r.list(t, "volume", "type", "list", "--long") {
		if field(row, "id") == tid {
			listed = true
			if p := field(row, "properties"); !strings.Contains(p, "ft:two") || strings.Contains(p, "ft:one") {
				t.Errorf("volume type list --long properties for %s = %q", tid, p)
			}
		}
	}
	if !listed {
		t.Errorf("volume type list --long does not list %s", tid)
	}

	// Retype onto the same backend needs no migration.
	vol := newVolume(t, r)
	r.ok(t, "volume", "set", vol, "--type", tid, "--wait")
	if got := field(r.show(t, "volume", "show", vol), "volume_type"); got != name+"-renamed" && got != tid {
		t.Errorf("volume_type after retype = %q", got)
	}

	qname := uniq("qos")
	q := r.show(t, "volume", "qos", "create", qname, "--consumer", "front-end", "--property", "read_iops_sec=100")
	qid := field(q, "id")
	t.Cleanup(func() { r.run(t, "volume", "qos", "delete", qid, "--force") })
	r.ok(t, "volume", "qos", "set", qid, "--property", "write_iops_sec=50")
	r.ok(t, "volume", "qos", "unset", qid, "--property", "read_iops_sec")
	q = r.show(t, "volume", "qos", "show", qid)
	if field(q, "consumer") != "front-end" || !strings.Contains(field(q, "properties"), "write_iops_sec") ||
		strings.Contains(field(q, "properties"), "read_iops_sec") {
		t.Errorf("after qos set/unset: %v", q)
	}
	if !slices.Contains(column(r.list(t, "volume", "qos", "list"), "id"), qid) {
		t.Errorf("volume qos list does not list %s", qid)
	}

	r.ok(t, "volume", "qos", "associate", qid, tid)
	if field(r.show(t, "volume", "type", "show", tid), "qos_specs_id") != qid {
		t.Errorf("type %s does not carry qos %s after associate", tid, qid)
	}
	// qos list names the associated type, as upstream's Associations.
	var assoc string
	for _, row := range r.list(t, "volume", "qos", "list") {
		if field(row, "id") == qid {
			assoc = field(row, "associations")
		}
	}
	if !strings.Contains(assoc, name+"-renamed") {
		t.Errorf("volume qos list associations for %s = %q, want %s", qid, assoc, name+"-renamed")
	}
	r.fails(t, "volume", "qos", "delete", qid) // still associated
	r.ok(t, "volume", "qos", "disassociate", qid, "--volume-type", tid)
	if got := field(r.show(t, "volume", "type", "show", tid), "qos_specs_id"); got != "" {
		t.Errorf("qos_specs_id after disassociate = %q", got)
	}
	r.ok(t, "volume", "qos", "delete", qid)

	r.ok(t, "volume", "delete", vol)
	gone(t, r, "volume", "show", vol)
	r.ok(t, "volume", "type", "delete", tid)
	r.fails(t, "volume", "type", "show", tid)
}

func TestVolumeServicesAndBackends(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)

	svcs := r.list(t, "volume", "service", "list", "--service", "cinder-scheduler", "--long")
	if len(svcs) != 1 {
		t.Fatalf("volume service list --service cinder-scheduler = %v", svcs)
	}
	host := field(svcs[0], "host")
	r.ok(t, "volume", "service", "set", host, "cinder-scheduler", "--disable", "--disable-reason", "koc functional")
	t.Cleanup(func() { r.run(t, "volume", "service", "set", host, "cinder-scheduler", "--enable") })
	if s := r.list(t, "volume", "service", "list", "--host", host, "--service", "cinder-scheduler"); len(s) != 1 || field(s[0], "status") != "disabled" {
		t.Errorf("after --disable: %v", s)
	}
	r.ok(t, "volume", "service", "set", host, "cinder-scheduler", "--enable")

	pool := lvmPool(t, r)
	if rows := r.list(t, "volume", "backend", "pool", "list", "--long"); len(rows) == 0 {
		t.Error("volume backend pool list --long is empty")
	}
	backendHost, _, _ := strings.Cut(pool, "#")
	if cp := r.show(t, "volume", "backend", "capability", "show", backendHost); len(cp) == 0 {
		t.Errorf("volume backend capability show %s is empty", backendHost)
	}

	// devstack does not cluster cinder-volume: the listing is empty, and a
	// cluster that is not there cannot be shown or set.
	r.ok(t, "block", "storage", "cluster", "list", "--long")
	r.fails(t, "block", "storage", "cluster", "show", "ft-no-such-cluster")
	r.fails(t, "block", "storage", "cluster", "set", "ft-no-such-cluster", "--enable")
}
