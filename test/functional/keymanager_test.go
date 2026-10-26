//go:build functional

package functional

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The barbican suite (up.sh's "keymanager" feature, simple_crypto backend):
// secrets through their payloads, secret consumers, containers, orders, and the
// ACLs of secrets and containers.

// newSecret stores a secret and deletes it at the end; extra are more `secret
// store` flags.
func newSecret(t *testing.T, r runner, extra ...string) (name, href string) {
	t.Helper()
	name = uniq("secret")
	s := r.show(t, append([]string{"secret", "store", "-n", name}, extra...)...)
	href = field(s, "Secret href")
	if href == "" {
		t.Fatalf("secret store returned no href: %v", s)
	}
	t.Cleanup(func() { r.run(t, "secret", "delete", href, "--force") })
	return name, href
}

func TestKeyManagerSecrets(t *testing.T) {
	c := ft(t)
	requireFeature(t, "keymanager")
	r := defaultRunner(c)

	name, href := newSecret(t, r, "--payload", "s3cr3t", "--payload-content-type", "text/plain")
	if s := r.show(t, "secret", "get", href); field(s, "Name") != name || field(s, "Status") != "ACTIVE" ||
		field(s, "Secret type") != "opaque" {
		t.Errorf("secret get: %v", s)
	}
	if p := field(r.show(t, "secret", "get", href, "--payload"), "Payload"); p != "s3cr3t" {
		t.Errorf("secret get --payload = %q, want s3cr3t", p)
	}
	path := filepath.Join(t.TempDir(), "payload")
	r.ok(t, "secret", "get", href, "--file", path)
	if b, err := os.ReadFile(path); err != nil || string(b) != "s3cr3t" {
		t.Errorf("secret get --file wrote %q (%v)", b, err)
	}
	if rows := r.list(t, "secret", "list", "--name", name); len(rows) != 1 || field(rows[0], "Secret href") != href {
		t.Errorf("secret list --name: %v", rows)
	}

	// Without --payload-content-type the payload goes up as base64 bytes, as
	// upstream sends it, and reads back as the same text.
	_, binHref := newSecret(t, r, "--payload", "bytes")
	if p := field(r.show(t, "secret", "get", binHref, "-p", "-t", "application/octet-stream"), "Payload"); p != "bytes" {
		t.Errorf("octet-stream payload = %q, want bytes", p)
	}

	// A secret stored without a payload takes one later, once.
	_, emptyHref := newSecret(t, r)
	r.ok(t, "secret", "update", emptyHref, "late payload")
	if p := field(r.show(t, "secret", "get", emptyHref, "--decrypt"), "Payload"); p != "late payload" {
		t.Errorf("payload after secret update = %q", p)
	}

	r.ok(t, "secret", "delete", href)
	r.fails(t, "secret", "get", href)
}

func TestKeyManagerSecretConsumers(t *testing.T) {
	c := ft(t)
	requireFeature(t, "keymanager")
	r := defaultRunner(c)
	_, href := newSecret(t, r, "--payload", "consumed", "-t", "text/plain")
	consumer := []string{"--service-type-name", "image", "--resource-type", "image",
		"--resource-id", "00000000-0000-4000-8000-000000000001"}

	r.ok(t, append([]string{"secret", "consumer", "create", href}, consumer...)...)
	rows := r.list(t, "secret", "consumer", "list", href)
	if len(rows) != 1 || field(rows[0], "Service") != "image" || field(rows[0], "Resource id") != consumer[5] {
		t.Fatalf("secret consumer list: %v", rows)
	}
	if msg := r.fails(t, "secret", "delete", href); !strings.Contains(msg, "consumers") {
		t.Errorf("deleting a consumed secret: %s", msg)
	}

	r.ok(t, append([]string{"secret", "consumer", "delete", href}, consumer...)...)
	if rows := r.list(t, "secret", "consumer", "list", href); len(rows) != 0 {
		t.Errorf("after secret consumer delete: %v", rows)
	}
}

func TestKeyManagerContainers(t *testing.T) {
	c := ft(t)
	requireFeature(t, "keymanager")
	r := defaultRunner(c)
	_, private := newSecret(t, r, "--payload", "private", "-t", "text/plain")
	_, public := newSecret(t, r, "--payload", "public", "-t", "text/plain")

	name := uniq("container")
	ct := r.show(t, "secret", "container", "create", "--type", "rsa", "--name", name,
		"--secret", "private_key="+private, "--secret", "public_key="+public)
	href := field(ct, "Container href")
	t.Cleanup(func() { r.run(t, "secret", "container", "delete", href) })
	if field(ct, "Type") != "rsa" || field(ct, "Public Key") != public || field(ct, "Private Key") != private {
		t.Errorf("secret container create: %v", ct)
	}
	if g := r.show(t, "secret", "container", "get", href); field(g, "Name") != name {
		t.Errorf("secret container get: %v", g)
	}
	rows := r.list(t, "secret", "container", "list", "--name", name, "--type", "rsa")
	if len(rows) != 1 || !strings.Contains(field(rows[0], "Secrets"), "public_key="+public) {
		t.Errorf("secret container list: %v", rows)
	}

	r.ok(t, "secret", "container", "delete", href)
	r.fails(t, "secret", "container", "get", href)
}

// orderActive waits for an order to finish and returns it.
func orderActive(t *testing.T, r runner, href string) map[string]any {
	t.Helper()
	var o map[string]any
	waitFor(t, "order "+href+" to go ACTIVE", 2*time.Minute, func() bool {
		o = r.show(t, "secret", "order", "get", href)
		if field(o, "Status") == "ERROR" {
			t.Fatalf("order went to ERROR: %v", o)
		}
		return field(o, "Status") == "ACTIVE"
	})
	return o
}

func TestKeyManagerOrders(t *testing.T) {
	c := ft(t)
	requireFeature(t, "keymanager")
	r := defaultRunner(c)

	key := r.show(t, "secret", "order", "create", "key", "--name", uniq("order"), "--bit-length", "128")
	keyHref := field(key, "Order href")
	t.Cleanup(func() { r.run(t, "secret", "order", "delete", keyHref) })
	if field(key, "Type") != "Key" || field(key, "Container href") != "N/A" {
		t.Errorf("secret order create key: %v", key)
	}
	done := orderActive(t, r, keyHref)
	secret := field(done, "Secret href")
	t.Cleanup(func() { r.run(t, "secret", "delete", secret) })
	if s := r.show(t, "secret", "get", secret); field(s, "Bit length") != "128" || field(s, "Algorithm") != "aes" {
		t.Errorf("the ordered key: %v", s)
	}

	asym := r.show(t, "secret", "order", "create", "asymmetric", "--algorithm", "rsa", "--bit-length", "2048")
	asymHref := field(asym, "Order href")
	t.Cleanup(func() { r.run(t, "secret", "order", "delete", asymHref) })
	if container := field(orderActive(t, r, asymHref), "Container href"); container == "" || container == "N/A" {
		t.Errorf("asymmetric order holds no container")
	} else {
		t.Cleanup(func() { r.run(t, "secret", "container", "delete", container) })
	}

	if hrefs := column(r.list(t, "secret", "order", "list", "--limit", "100"), "Order href"); !slices.Contains(hrefs, keyHref) {
		t.Errorf("secret order list lacks %s: %v", keyHref, hrefs)
	}
	r.ok(t, "secret", "order", "delete", keyHref)
	r.fails(t, "secret", "order", "get", keyHref)
}

// aclRead returns the "read" row of `acl get`.
func aclRead(t *testing.T, r runner, href string) map[string]any {
	t.Helper()
	for _, row := range r.list(t, "acl", "get", href) {
		if field(row, "Operation Type") == "read" {
			return row
		}
	}
	t.Fatalf("acl get %s has no read row", href)
	return nil
}

// aclUsers is a row's Users column, sorted.
func aclUsers(row map[string]any) []string {
	var out []string
	if users, ok := row["Users"].([]any); ok {
		for _, u := range users {
			if s, ok := u.(string); ok {
				out = append(out, s)
			}
		}
	}
	slices.Sort(out)
	return out
}

func TestKeyManagerACLs(t *testing.T) {
	c := ft(t)
	requireFeature(t, "keymanager")
	r := defaultRunner(c)
	_, href := newSecret(t, r, "--payload", "shared", "-t", "text/plain")

	if row := aclRead(t, r, href); field(row, "Project Access") != "true" || field(row, "Secret ACL Ref") != href+"/acl" {
		t.Errorf("default ACL: %v", row)
	}
	// Barbican does not check that an ACL user exists, so synthetic IDs serve.
	r.ok(t, "acl", "submit", href, "--user", "ft-user-1", "--no-project-access")
	if row := aclRead(t, r, href); field(row, "Project Access") != "false" || !slices.Equal(aclUsers(row), []string{"ft-user-1"}) {
		t.Errorf("after acl submit: %v", row)
	}
	r.ok(t, "acl", "user", "add", href, "--user", "ft-user-2")
	if got := aclUsers(aclRead(t, r, href)); !slices.Equal(got, []string{"ft-user-1", "ft-user-2"}) {
		t.Errorf("after acl user add: %v", got)
	}
	r.ok(t, "acl", "user", "remove", href, "--user", "ft-user-1", "--project-access")
	if row := aclRead(t, r, href); field(row, "Project Access") != "true" || !slices.Equal(aclUsers(row), []string{"ft-user-2"}) {
		t.Errorf("after acl user remove: %v", row)
	}
	r.ok(t, "acl", "delete", href)
	if row := aclRead(t, r, href); field(row, "Project Access") != "true" || len(aclUsers(row)) != 0 {
		t.Errorf("after acl delete: %v", row)
	}

	// A container's ACL takes the same verbs.
	_, member := newSecret(t, r, "--payload", "member", "-t", "text/plain")
	ct := r.show(t, "secret", "container", "create", "--name", uniq("acl"), "--secret", "member="+member)
	cHref := field(ct, "Container href")
	t.Cleanup(func() { r.run(t, "secret", "container", "delete", cHref) })
	r.ok(t, "acl", "submit", cHref, "--user", "ft-user-3")
	if row := aclRead(t, r, cHref); !slices.Equal(aclUsers(row), []string{"ft-user-3"}) || field(row, "Container ACL Ref") != cHref+"/acl" {
		t.Errorf("container ACL: %v", row)
	}
	r.ok(t, "acl", "delete", cHref)
}
