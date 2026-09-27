//go:build functional

package functional

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The glance suite: an image from upload to download, its visibility and
// membership, the staging path, and glance's discovery endpoints.

// payload writes n random bytes to a file and returns its path and content.
func payload(t *testing.T, n int) (string, []byte) {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return writeFile(t, t.TempDir(), "image.raw", string(b)), b
}

// waitFor polls fn until it returns true or the deadline passes.
func waitFor(t *testing.T, what string, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(2 * time.Second)
	}
}

func TestImageLifecycle(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)

	file, content := payload(t, 1<<20)
	name := uniq("img")
	img := r.show(t, "image", "create", name, "--file", file, "--disk-format", "raw", "--container-format", "bare",
		"--private", "--property", "ft=1", "--tag", "ft-tag", "--min-ram", "64")
	id := field(img, "id")
	t.Cleanup(func() {
		r.run(t, "image", "set", id, "--unprotected")
		r.run(t, "image", "delete", id)
	})
	if field(img, "status") != "active" || field(img, "visibility") != "private" || field(img, "size") != "1048576" {
		t.Fatalf("image create = %v", img)
	}

	renamed := name + "-renamed"
	r.ok(t, "image", "set", id, "--name", renamed, "--min-disk", "1", "--property", "ft2=2", "--protected")
	img = r.show(t, "image", "show", id)
	if field(img, "name") != renamed || field(img, "min_disk") != "1" || field(img, "protected") != "true" ||
		!strings.Contains(field(img, "properties"), "ft2") {
		t.Errorf("after image set: %v", img)
	}
	r.fails(t, "image", "delete", id) // protected
	r.ok(t, "image", "set", id, "--unprotected")
	r.ok(t, "image", "unset", id, "--property", "ft2")
	if img = r.show(t, "image", "show", id); strings.Contains(field(img, "properties"), "ft2") {
		t.Errorf("after image unset --property ft2: %v", img)
	}

	r.ok(t, "image", "set", id, "--deactivate")
	if s := field(r.show(t, "image", "show", id), "status"); s != "deactivated" {
		t.Errorf("status after --deactivate = %q", s)
	}
	r.ok(t, "image", "set", id, "--activate")

	if rows := r.list(t, "image", "list", "--name", renamed); len(rows) != 1 {
		t.Errorf("image list --name = %v", rows)
	}
	if !slices.Contains(column(r.list(t, "image", "list", "--private"), "id"), id) {
		t.Errorf("image list --private does not list %s", id)
	}

	out := filepath.Join(t.TempDir(), "saved.raw")
	r.ok(t, "image", "save", id, "--file", out)
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, content) {
		t.Errorf("image save: %d bytes, err %v; want the %d uploaded", len(got), err, len(content))
	}
}

// Sharing: the owner adds a project as a member, the member accepts, and the
// image then shows up for it.
func TestImageMembership(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)

	file, _ := payload(t, 4096)
	img := r.show(t, "image", "create", uniq("shared"), "--file", file, "--disk-format", "raw",
		"--container-format", "bare", "--shared")
	id := field(img, "id")
	t.Cleanup(func() { r.run(t, "image", "delete", id) })

	demo := ""
	for _, p := range r.list(t, "project", "list") {
		if field(p, "name") == "demo" {
			demo = field(p, "id")
		}
	}
	r.ok(t, "image", "add", "project", id, demo)
	if rows := r.list(t, "image", "member", "list", id); !in(rows, demo) || !in(rows, "pending") {
		t.Errorf("image member list = %v, want %s pending", rows, demo)
	}

	// Acceptance is the member's own call. devstack's demo user holds demo.
	asDemo := demoRunner(c)
	asDemo.ok(t, "image", "member", "set", id, demo, "--accept")
	if rows := r.list(t, "image", "member", "list", id); !in(rows, "accepted") {
		t.Errorf("after --accept, member list = %v", rows)
	}
	if !slices.Contains(column(asDemo.list(t, "image", "list", "--shared"), "id"), id) {
		t.Errorf("the accepted image is not in demo's image list --shared")
	}

	r.ok(t, "image", "remove", "project", id, demo)
	if rows := r.list(t, "image", "member", "list", id); in(rows, demo) {
		t.Errorf("after remove project, member list = %v", rows)
	}
}

// The staged half of interoperable import, and the import it feeds.
func TestImageStageAndImport(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	r := defaultRunner(c)

	info := r.show(t, "image", "import", "info")
	if !strings.Contains(field(info, "import-methods"), "glance-direct") {
		t.Fatalf("image import info = %v, want glance-direct among the methods", info)
	}

	file, _ := payload(t, 8192)
	img := r.show(t, "image", "create", uniq("staged"), "--disk-format", "raw", "--container-format", "bare")
	id := field(img, "id")
	t.Cleanup(func() { r.run(t, "image", "delete", id) })
	r.ok(t, "image", "stage", id, "--file", file)
	if s := field(r.show(t, "image", "show", id), "status"); s != "uploading" {
		t.Errorf("status after stage = %q, want uploading", s)
	}

	// create --import stages and imports in one go (glance-direct).
	imp := r.show(t, "image", "create", uniq("imported"), "--file", file, "--disk-format", "raw",
		"--container-format", "bare", "--import")
	impID := field(imp, "id")
	t.Cleanup(func() { r.run(t, "image", "delete", impID) })
	waitFor(t, "the imported image to go active", 3*time.Minute, func() bool {
		return field(r.show(t, "image", "show", impID), "status") == "active"
	})

	// The import ran as a glance task.
	tasks := r.list(t, "image", "task", "list", "--type", "api_image_import")
	if len(tasks) == 0 {
		t.Fatalf("image task list --type api_image_import is empty after an import")
	}
	task := r.show(t, "image", "task", "show", field(tasks[0], "id"))
	if field(task, "type") != "api_image_import" {
		t.Errorf("image task show = %v", task)
	}
}

// glance lists its stores only when multi-store is configured; otherwise it
// answers 404. Both are a working koc, and which one this cloud gives is part of
// what the test records.
func TestImageStores(t *testing.T) {
	c := ft(t)
	requireFeature(t, "core")
	res := defaultRunner(c).run(t, "image", "stores", "list", "-f", "json")
	switch {
	case res.code == 0:
		if !strings.Contains(res.stdout, "id") {
			t.Errorf("image stores list = %s, want at least one store", res.stdout)
		}
	case strings.Contains(strings.ToLower(res.stderr), "404") || strings.Contains(strings.ToLower(res.stderr), "multi"):
		t.Logf("glance is single-store here: %s", strings.TrimSpace(res.stderr))
	default:
		t.Errorf("image stores list: exit %d: %s", res.code, res.stderr)
	}
}
