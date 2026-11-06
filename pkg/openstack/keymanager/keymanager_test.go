package keymanager_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/acls"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/containers"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/orders"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/secrets"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/koc/pkg/openstack/keymanager"
)

const (
	sid   = "11111111-2222-3333-4444-555555555555"
	cid   = "66666666-7777-8888-9999-aaaaaaaaaaaa"
	oid   = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
	base  = "https://cloud.example.com/key-manager/v1/"
	sHref = base + "secrets/" + sid
	cHref = base + "containers/" + cid
	oHref = base + "orders/" + oid
)

type reply struct {
	status int
	body   string
}

// seen is one request the fake answered.
type seen struct {
	key, body, version, accept, contentType string
}

// newFake answers "METHOD /path[?query]" from routes (each a sequence; the
// last reply repeats) and records every request.
func newFake(t *testing.T, routes map[string][]reply) (*[]seen, *gophercloud.ServiceClient) {
	t.Helper()
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	var log []seen
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		b, _ := io.ReadAll(r.Body)
		log = append(log, seen{key, string(b), r.Header.Get("OpenStack-API-Version"), r.Header.Get("Accept"), r.Header.Get("Content-Type")})
		seq, ok := routes[key]
		if !ok {
			t.Errorf("unexpected request %s", key)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		rep := seq[0]
		if len(seq) > 1 {
			routes[key] = seq[1:]
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	})
	sc := fakeclient.ServiceClient(fs)
	sc.Type = "key-manager"
	sc.Microversion = "1.1"
	sc.ResourceBase = sc.Endpoint + "v1/"
	return &log, sc
}

func ok(body string) []reply { return []reply{{http.StatusOK, body}} }

var fail = []reply{{http.StatusInternalServerError, `{}`}}

func wantErr(t *testing.T, err error, prefix string) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("err = %v, want prefix %q", err, prefix)
	}
}

func TestRefUUID(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{sHref, sid},
		{sHref + "/", sid},
		{sid, sid},
		{strings.ToUpper(strings.ReplaceAll(sid, "-", "")), sid},
	} {
		got, err := keymanager.RefUUID(tc.in, "Secret")
		if err != nil || got != tc.want {
			t.Errorf("RefUUID(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "db-password", base + "secrets/"} {
		if _, err := keymanager.RefUUID(bad, "Secret"); err == nil || err.Error() != "Secret incorrectly specified" {
			t.Errorf("RefUUID(%q) error = %v", bad, err)
		}
	}
}

func TestParseACLRef(t *testing.T) {
	got, err := keymanager.ParseACLRef(cHref + "/")
	if want := (keymanager.ACLTarget{Ref: cHref, ID: cid, Container: true}); err != nil || got != want {
		t.Errorf("container href: %+v, %v", got, err)
	}
	got, err = keymanager.ParseACLRef(sHref)
	if want := (keymanager.ACLTarget{Ref: sHref, ID: sid}); err != nil || got != want {
		t.Errorf("secret href: %+v, %v", got, err)
	}
	if _, err := keymanager.ParseACLRef(sid); err == nil {
		t.Error("a bare UUID was accepted")
	}
	if _, err := keymanager.ParseACLRef(base + "secrets/nope"); err == nil || err.Error() != "Secret incorrectly specified" {
		t.Errorf("bad UUID: %v", err)
	}
}

func TestSecrets(t *testing.T) {
	one := `{"secret_ref":"` + sHref + `","name":"db","bit_length":256,"consumers":[]}`
	log, c := newFake(t, map[string][]reply{
		"GET /v1/secrets?limit=2&name=db&offset=4": ok(`{"secrets":[` + one + `],"total":1}`),
		"GET /v1/secrets?limit=10":                 ok(`{"secrets":[],"total":0}`),
		"GET /v1/secrets/" + sid:                   ok(one),
		"GET /v1/secrets/" + sid + "/payload":      ok(`hunter2`),
		"POST /v1/secrets":                         {{http.StatusCreated, `{"secret_ref":"` + sHref + `"}`}, {http.StatusCreated, `{"secret_ref":"nope"}`}},
		"PUT /v1/secrets/" + sid:                   {{http.StatusNoContent, ``}},
	})
	ctx := context.Background()

	list, err := keymanager.ListSecrets(ctx, c, secrets.ListOpts{Limit: 2, Offset: 4, Name: "db"})
	if err != nil || len(list) != 1 || list[0].Name != "db" || list[0].BitLength != 256 {
		t.Fatalf("ListSecrets = %+v, %v", list, err)
	}
	if (*log)[0].version != "key-manager 1.1" {
		t.Errorf("microversion header = %q", (*log)[0].version)
	}
	if list, err := keymanager.ListSecrets(ctx, c, secrets.ListOpts{Limit: 10}); err != nil || len(list) != 0 {
		t.Errorf("empty ListSecrets = %+v, %v", list, err)
	}
	if s, err := keymanager.GetSecret(ctx, c, sid); err != nil || s.SecretRef != sHref {
		t.Errorf("GetSecret = %+v, %v", s, err)
	}
	if p, err := keymanager.GetSecretPayload(ctx, c, sid, "text/plain"); err != nil || string(p) != "hunter2" {
		t.Errorf("GetSecretPayload = %q, %v", p, err)
	}
	if l := (*log)[len(*log)-1]; l.accept != "text/plain" {
		t.Errorf("payload Accept = %q", l.accept)
	}

	id, err := keymanager.CreateSecret(ctx, c, secrets.CreateOpts{Name: "db", SecretType: secrets.OpaqueSecret})
	if err != nil || id != sid {
		t.Errorf("CreateSecret = %q, %v", id, err)
	}
	th.CheckJSONEquals(t, `{"name":"db","secret_type":"opaque"}`, rawJSON((*log)[len(*log)-1].body))
	_, err = keymanager.CreateSecret(ctx, c, secrets.CreateOpts{Name: "db"})
	wantErr(t, err, `storing secret: barbican answered secret_ref "nope": Secret incorrectly specified`)

	if err := keymanager.SetSecretPayload(ctx, c, sid, "hunter2"); err != nil {
		t.Fatal(err)
	}
	if l := (*log)[len(*log)-1]; l.body != "hunter2" || l.contentType != "text/plain" {
		t.Errorf("payload PUT = %+v", l)
	}
}

// rawJSON makes a recorded body comparable with th.CheckJSONEquals.
func rawJSON(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}

func TestDeleteSecret(t *testing.T) {
	path := "/v1/secrets/" + sid
	log, c := newFake(t, map[string][]reply{
		"GET " + path:    ok(`{"consumers":[{"service":"image"}]}`),
		"DELETE " + path: {{http.StatusNoContent, ``}},
	})
	ctx := context.Background()
	if err := keymanager.DeleteSecret(ctx, c, sid, false); !errors.Is(err, keymanager.ErrSecretHasConsumers) {
		t.Errorf("err = %v, want ErrSecretHasConsumers", err)
	}
	if len(*log) != 1 {
		t.Errorf("refusal sent %d requests", len(*log))
	}
	if err := keymanager.DeleteSecret(ctx, c, sid, true); err != nil {
		t.Fatal(err)
	}
	if (*log)[2].key != "DELETE "+path {
		t.Errorf("requests = %+v", *log)
	}
}

func TestSecretErrors(t *testing.T) {
	path := "/v1/secrets/" + sid
	_, c := newFake(t, map[string][]reply{
		"GET /v1/secrets?limit=1":                      fail,
		"GET " + path:                                  fail,
		"GET " + path + "/payload":                     fail,
		"POST /v1/secrets":                             fail,
		"PUT " + path:                                  fail,
		"POST " + path + "/consumers":                  fail,
		"GET " + path + "/consumers?limit=10&offset=0": fail,
	})
	ctx := context.Background()
	_, err := keymanager.ListSecrets(ctx, c, secrets.ListOpts{Limit: 1})
	wantErr(t, err, "listing secrets: ")
	_, err = keymanager.GetSecret(ctx, c, sid)
	wantErr(t, err, "getting secret "+sid+": ")
	_, err = keymanager.GetSecretPayload(ctx, c, sid, "text/plain")
	wantErr(t, err, "getting payload of secret "+sid+": ")
	_, err = keymanager.CreateSecret(ctx, c, secrets.CreateOpts{})
	wantErr(t, err, "storing secret: ")
	wantErr(t, keymanager.SetSecretPayload(ctx, c, sid, "x"), "updating secret "+sid+": ")
	wantErr(t, keymanager.DeleteSecret(ctx, c, sid, true), "getting secret "+sid+": ")
	wantErr(t, keymanager.AddSecretConsumer(ctx, c, sid, keymanager.SecretConsumer{}), "registering consumer of secret "+sid+": ")
	_, err = keymanager.ListSecretConsumers(ctx, c, sid, 10, 0)
	wantErr(t, err, "listing consumers of secret "+sid+": ")
}

func TestSecretConsumers(t *testing.T) {
	path := "/v1/secrets/" + sid + "/consumers"
	log, c := newFake(t, map[string][]reply{
		"POST " + path:                      {{http.StatusCreated, `{}`}},
		"DELETE " + path:                    {{http.StatusNoContent, ``}, {http.StatusNotFound, `{}`}},
		"GET " + path + "?limit=5&offset=1": ok(`{"consumers":[{"service":"image","resource_type":"image","resource_id":"img-1","created":"2026-01-02T03:04:05"}],"total":1}`),
	})
	ctx := context.Background()
	cons := keymanager.SecretConsumer{Service: "image", ResourceType: "image", ResourceID: "img-1", Created: "ignored"}
	if err := keymanager.AddSecretConsumer(ctx, c, sid, cons); err != nil {
		t.Fatal(err)
	}
	want := `{"service":"image","resource_type":"image","resource_id":"img-1"}`
	th.CheckJSONEquals(t, want, rawJSON((*log)[0].body))
	if err := keymanager.RemoveSecretConsumer(ctx, c, sid, cons); err != nil {
		t.Fatal(err)
	}
	th.CheckJSONEquals(t, want, rawJSON((*log)[1].body))
	wantErr(t, keymanager.RemoveSecretConsumer(ctx, c, sid, cons), "removing consumer from secret "+sid+": ")

	list, err := keymanager.ListSecretConsumers(ctx, c, sid, 5, 1)
	if err != nil || len(list) != 1 || list[0].ResourceID != "img-1" || list[0].Created != "2026-01-02T03:04:05" {
		t.Errorf("ListSecretConsumers = %+v, %v", list, err)
	}
}

func TestContainers(t *testing.T) {
	one := `{"container_ref":"` + cHref + `","name":"tls","type":"certificate"}`
	log, c := newFake(t, map[string][]reply{
		"GET /v1/containers?limit=10&type=rsa": ok(`{"containers":[` + one + `],"total":1}`),
		"GET /v1/containers?limit=1":           {{http.StatusOK, `{"containers":[],"total":0}`}, fail[0]},
		"GET /v1/containers/" + cid:            {{http.StatusOK, one}, fail[0]},
		"POST /v1/containers":                  {{http.StatusCreated, `{"container_ref":"` + cHref + `"}`}, {http.StatusCreated, `{"container_ref":""}`}, fail[0]},
		"DELETE /v1/containers/" + cid:         {{http.StatusNoContent, ``}, fail[0]},
	})
	ctx := context.Background()
	list, err := keymanager.ListContainers(ctx, c, keymanager.ContainerListOpts{Limit: 10, Type: "rsa"})
	if err != nil || len(list) != 1 || list[0].Name != "tls" {
		t.Errorf("ListContainers = %+v, %v", list, err)
	}
	if list, err := keymanager.ListContainers(ctx, c, keymanager.ContainerListOpts{Limit: 1}); err != nil || list != nil {
		t.Errorf("empty ListContainers = %+v, %v", list, err)
	}
	_, err = keymanager.ListContainers(ctx, c, keymanager.ContainerListOpts{Limit: 1})
	wantErr(t, err, "listing containers: ")

	if r, err := keymanager.GetContainer(ctx, c, cid); err != nil || r.Type != "certificate" {
		t.Errorf("GetContainer = %+v, %v", r, err)
	}
	_, err = keymanager.GetContainer(ctx, c, cid)
	wantErr(t, err, "getting container "+cid+": ")

	opts := containers.CreateOpts{Name: "tls", Type: containers.GenericContainer,
		SecretRefs: []containers.SecretRef{{Name: "a", SecretRef: sHref}}}
	if id, err := keymanager.CreateContainer(ctx, c, opts); err != nil || id != cid {
		t.Errorf("CreateContainer = %q, %v", id, err)
	}
	th.CheckJSONEquals(t, `{"name":"tls","type":"generic","secret_refs":[{"name":"a","secret_ref":"`+sHref+`"}]}`,
		rawJSON((*log)[len(*log)-1].body))
	_, err = keymanager.CreateContainer(ctx, c, opts)
	wantErr(t, err, `creating container: barbican answered container_ref "": `)
	_, err = keymanager.CreateContainer(ctx, c, opts)
	wantErr(t, err, "creating container: ")

	if err := keymanager.DeleteContainer(ctx, c, cid); err != nil {
		t.Fatal(err)
	}
	wantErr(t, keymanager.DeleteContainer(ctx, c, cid), "deleting container "+cid+": ")
}

func TestOrders(t *testing.T) {
	one := `{"order_ref":"` + oHref + `","type":"key","status":"ACTIVE"}`
	log, c := newFake(t, map[string][]reply{
		"GET /v1/orders?limit=3&offset=6": {{http.StatusOK, `{"orders":[` + one + `],"total":1}`}, fail[0]},
		"GET /v1/orders":                  ok(`{"orders":[],"total":0}`),
		"GET /v1/orders/" + oid:           {{http.StatusOK, one}, fail[0]},
		"POST /v1/orders":                 {{http.StatusAccepted, `{"order_ref":"` + oHref + `"}`}, {http.StatusAccepted, `{"order_ref":"x"}`}, fail[0]},
		"DELETE /v1/orders/" + oid:        {{http.StatusNoContent, ``}, fail[0]},
	})
	ctx := context.Background()
	list, err := keymanager.ListOrders(ctx, c, orders.ListOpts{Limit: 3, Offset: 6})
	if err != nil || len(list) != 1 || list[0].Status != "ACTIVE" {
		t.Errorf("ListOrders = %+v, %v", list, err)
	}
	_, err = keymanager.ListOrders(ctx, c, orders.ListOpts{Limit: 3, Offset: 6})
	wantErr(t, err, "listing orders: ")
	if list, err := keymanager.ListOrders(ctx, c, orders.ListOpts{}); err != nil || len(list) != 0 {
		t.Errorf("empty ListOrders = %+v, %v", list, err)
	}

	if r, err := keymanager.GetOrder(ctx, c, oid); err != nil || r.Type != "key" {
		t.Errorf("GetOrder = %+v, %v", r, err)
	}
	_, err = keymanager.GetOrder(ctx, c, oid)
	wantErr(t, err, "getting order "+oid+": ")

	opts := orders.CreateOpts{Type: orders.KeyOrder, Meta: orders.MetaOpts{Algorithm: "aes", BitLength: 256}}
	if id, err := keymanager.CreateOrder(ctx, c, opts); err != nil || id != oid {
		t.Errorf("CreateOrder = %q, %v", id, err)
	}
	th.CheckJSONEquals(t, `{"type":"key","meta":{"algorithm":"aes","bit_length":256,"mode":""}}`, rawJSON((*log)[len(*log)-1].body))
	_, err = keymanager.CreateOrder(ctx, c, opts)
	wantErr(t, err, `creating order: barbican answered order_ref "x": Order incorrectly specified`)
	_, err = keymanager.CreateOrder(ctx, c, opts)
	wantErr(t, err, "creating order: ")

	if err := keymanager.DeleteOrder(ctx, c, oid); err != nil {
		t.Fatal(err)
	}
	wantErr(t, keymanager.DeleteOrder(ctx, c, oid), "deleting order "+oid+": ")
}

func aclOf(t *testing.T, s string) acls.ACL {
	t.Helper()
	var a acls.ACL
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

// setBody renders SetOpts as the PUT body barbican is sent.
func setBody(t *testing.T, opts acls.SetOpts) any {
	t.Helper()
	m, err := opts.ToACLSetMap()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestACLOpts(t *testing.T) {
	current := aclOf(t, `{"read": {"project-access": true, "users": ["u1", "u2"]}}`)
	yes, no := true, false

	got := keymanager.AddACLUsersOpts(current, keymanager.ACLChange{OperationType: "read", Users: []string{"u2", "u3"}, ProjectAccess: &no})
	th.CheckJSONEquals(t, `{"read": {"users": ["u1", "u2", "u3"], "project-access": false}}`, setBody(t, got))

	got = keymanager.RemoveACLUsersOpts(current, keymanager.ACLChange{OperationType: "read", Users: []string{"u1", "zz"}})
	th.CheckJSONEquals(t, `{"read": {"users": ["u2"], "project-access": true}}`, setBody(t, got))

	got = keymanager.RemoveACLUsersOpts(current, keymanager.ACLChange{OperationType: "read", ProjectAccess: &no})
	th.CheckJSONEquals(t, `{"read": {"users": ["u1", "u2"], "project-access": false}}`, setBody(t, got))

	got = keymanager.SubmitACLOpts(keymanager.ACLChange{OperationType: "read", ProjectAccess: &yes})
	th.CheckJSONEquals(t, `{"read": {"users": [], "project-access": true}}`, setBody(t, got))

	got = keymanager.AddACLUsersOpts(acls.ACL{}, keymanager.ACLChange{OperationType: "read", Users: []string{"u1"}})
	th.CheckJSONEquals(t, `{"read": {"users": ["u1"]}}`, setBody(t, got))

	if current["read"].Users[0] != "u1" || len(current["read"].Users) != 2 {
		t.Errorf("the current ACL was modified: %+v", current)
	}
}

func TestACL(t *testing.T) {
	sACL, cACL := "/v1/secrets/"+sid+"/acl", "/v1/containers/"+cid+"/acl"
	readACL := `{"read": {"project-access": false, "users": ["u1"]}}`
	log, c := newFake(t, map[string][]reply{
		"GET " + sACL:    ok(readACL),
		"PUT " + sACL:    ok(`{"acl_ref": "` + sHref + `/acl"}`),
		"DELETE " + sACL: ok(``),
		"GET " + cACL:    {{http.StatusOK, `{}`}, fail[0]},
		"PUT " + cACL:    {{http.StatusOK, `{"acl_ref": "` + cHref + `/acl"}`}, fail[0]},
		"DELETE " + cACL: {{http.StatusOK, ``}, fail[0]},
	})
	ctx := context.Background()
	st := keymanager.ACLTarget{Ref: sHref, ID: sid}
	ct := keymanager.ACLTarget{Ref: cHref, ID: cid, Container: true}

	a, err := keymanager.GetACL(ctx, c, st)
	if err != nil || a["read"].ProjectAccess || a["read"].Users[0] != "u1" {
		t.Errorf("GetACL(secret) = %+v, %v", a, err)
	}
	if a, err := keymanager.GetACL(ctx, c, ct); err != nil || a == nil || len(a) != 0 {
		t.Errorf("GetACL(container) = %#v, %v", a, err)
	}
	if err := keymanager.AddACLUsers(ctx, c, st, keymanager.ACLChange{OperationType: "read", Users: []string{"u2"}}); err != nil {
		t.Fatal(err)
	}
	th.CheckJSONEquals(t, `{"read": {"users": ["u1", "u2"], "project-access": false}}`, rawJSON((*log)[len(*log)-1].body))
	if err := keymanager.RemoveACLUsers(ctx, c, st, keymanager.ACLChange{OperationType: "read", Users: []string{"u1"}}); err != nil {
		t.Fatal(err)
	}
	th.CheckJSONEquals(t, `{"read": {"users": [], "project-access": false}}`, rawJSON((*log)[len(*log)-1].body))
	if err := keymanager.SetACL(ctx, c, ct, keymanager.SubmitACLOpts(keymanager.ACLChange{OperationType: "read"})); err != nil {
		t.Fatal(err)
	}
	if err := keymanager.DeleteACL(ctx, c, st); err != nil {
		t.Fatal(err)
	}
	if err := keymanager.DeleteACL(ctx, c, ct); err != nil {
		t.Fatal(err)
	}

	wantErr(t, keymanager.AddACLUsers(ctx, c, ct, keymanager.ACLChange{OperationType: "read"}), "getting ACL of "+cHref+": ")
	wantErr(t, keymanager.RemoveACLUsers(ctx, c, ct, keymanager.ACLChange{OperationType: "read"}), "getting ACL of "+cHref+": ")
	wantErr(t, keymanager.SetACL(ctx, c, ct, nil), "setting ACL of "+cHref+": ")
	wantErr(t, keymanager.DeleteACL(ctx, c, ct), "deleting ACL of "+cHref+": ")
}
