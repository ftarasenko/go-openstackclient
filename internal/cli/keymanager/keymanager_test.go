package keymanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/acls"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/orders"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const (
	secretID    = "11111111-2222-3333-4444-555555555555"
	containerID = "66666666-7777-8888-9999-aaaaaaaaaaaa"
	orderID     = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
	secretHref  = "https://cloud.example.com/key-manager/v1/secrets/" + secretID
	contHref    = "https://cloud.example.com/key-manager/v1/containers/" + containerID
)

// kmClient is a service client wired to the mock server the way
// auth.Client.KeyManager builds one: type key-manager, microversion 1.1, the
// v1 API under ResourceBase.
func kmClient(fs th.FakeServer) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fs)
	sc.Type = "key-manager"
	sc.Microversion = "1.1"
	sc.ResourceBase = sc.Endpoint + "v1/"
	return sc
}

// request is what a handler saw.
type request struct {
	method, query, apiVersion, contentType, accept string
	body                                           string
}

func record(t *testing.T, got *[]request, status int, reply string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		b, _ := io.ReadAll(r.Body)
		*got = append(*got, request{
			method: r.Method, query: r.URL.RawQuery, apiVersion: r.Header.Get("OpenStack-API-Version"),
			contentType: r.Header.Get("Content-Type"), accept: r.Header.Get("Accept"), body: string(b),
		})
		if reply != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}
}

func jsonOut() *output.Options { return &output.Options{Format: output.FormatJSON} }

const secretBody = `{
  "secret_ref": "` + secretHref + `",
  "name": "db-password",
  "created": "2026-01-02T03:04:05",
  "updated": "2026-01-02T03:04:05",
  "status": "ACTIVE",
  "content_types": {"default": "text/plain"},
  "algorithm": "aes",
  "bit_length": 256,
  "secret_type": "opaque",
  "mode": "cbc",
  "expiration": null,
  "consumers": []
}`

func TestRefUUID(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{secretHref, secretID},
		{secretHref + "/", secretID},
		{secretID, secretID},
		{strings.ToUpper(strings.ReplaceAll(secretID, "-", "")), secretID},
	} {
		got, err := refUUID(tc.in, "Secret")
		if err != nil || got != tc.want {
			t.Errorf("refUUID(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "db-password", "https://cloud.example.com/v1/secrets/"} {
		if _, err := refUUID(bad, "Secret"); err == nil || err.Error() != "Secret incorrectly specified" {
			t.Errorf("refUUID(%q) error = %v, want %q", bad, err, "Secret incorrectly specified")
		}
	}
}

func TestRunSecretList(t *testing.T) {
	fs := th.SetupHTTP()
	defer fs.Teardown()
	var got []request
	fs.Mux.HandleFunc("/v1/secrets", record(t, &got, http.StatusOK,
		`{"secrets": [`+secretBody+`], "total": 3, "next": "`+fs.Endpoint()+`v1/secrets?limit=1&offset=1"}`))

	var buf bytes.Buffer
	f := &secretListFlags{limit: 1, name: "db-password", algorithm: "aes", bitLength: 256, secretType: "opaque"}
	if err := runSecretList(context.Background(), kmClient(fs), jsonOut(), f, &buf); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("requests = %d, want 1: the list must not follow next", len(got))
	}
	if got[0].method != http.MethodGet || got[0].apiVersion != "key-manager 1.1" {
		t.Errorf("request = %+v", got[0])
	}
	for _, want := range []string{"limit=1", "name=db-password", "alg=aes", "bits=256", "secret_type=opaque"} {
		if !strings.Contains(got[0].query, want) {
			t.Errorf("query %q lacks %q", got[0].query, want)
		}
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, buf.String())
	}
	if len(rows) != 1 || rows[0]["Secret href"] != secretHref || rows[0]["Created"] != "2026-01-02T03:04:05+00:00" ||
		rows[0]["Bit length"] != float64(256) || rows[0]["Expiration"] != nil {
		t.Errorf("rows = %v", rows)
	}
}

func TestRunSecretList_Empty(t *testing.T) {
	fs := th.SetupHTTP()
	defer fs.Teardown()
	var got []request
	fs.Mux.HandleFunc("/v1/secrets", record(t, &got, http.StatusOK, `{"secrets": [], "total": 0}`))
	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatCSV}
	if err := runSecretList(context.Background(), kmClient(fs), o, &secretListFlags{limit: 10}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "Secret href,Name,") || strings.Count(buf.String(), "\n") != 1 {
		t.Errorf("output = %q, want the header only", buf.String())
	}
}

func TestRunSecretGet(t *testing.T) {
	fs := th.SetupHTTP()
	defer fs.Teardown()
	var got []request
	fs.Mux.HandleFunc("/v1/secrets/"+secretID, record(t, &got, http.StatusOK, secretBody))

	var buf bytes.Buffer
	if err := runSecretGet(context.Background(), kmClient(fs), jsonOut(), secretHref, &secretGetFlags{}, &buf); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["Name"] != "db-password" || m["Status"] != "ACTIVE" || m["Mode"] != "cbc" {
		t.Errorf("fields = %v", m)
	}
	if ct, _ := m["Content types"].(map[string]any); ct["default"] != "text/plain" {
		t.Errorf("Content types = %v", m["Content types"])
	}
}

func TestRunSecretGet_Payload(t *testing.T) {
	fs := th.SetupHTTP()
	defer fs.Teardown()
	var got []request
	fs.Mux.HandleFunc("/v1/secrets/"+secretID+"/payload", func(w http.ResponseWriter, r *http.Request) {
		got = append(got, request{method: r.Method, accept: r.Header.Get("Accept")})
		_, _ = w.Write([]byte("s3cr3t"))
	})

	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatValue}
	f := &secretGetFlags{payload: true, payloadContentType: "application/octet-stream"}
	if err := runSecretGet(context.Background(), kmClient(fs), o, secretID, f, &buf); err != nil {
		t.Fatal(err)
	}
	if got[0].accept != "application/octet-stream" {
		t.Errorf("Accept = %q", got[0].accept)
	}
	if strings.TrimSpace(buf.String()) != "s3cr3t" {
		t.Errorf("output = %q", buf.String())
	}

	path := filepath.Join(t.TempDir(), "payload")
	f = &secretGetFlags{file: path, payloadContentType: "text/plain"}
	if err := runSecretGet(context.Background(), kmClient(fs), o, secretID, f, io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "s3cr3t" {
		t.Errorf("file holds %q", b)
	}
	err := runSecretGet(context.Background(), kmClient(fs), o, secretID, f, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second --file write: err = %v, want a refusal to overwrite", err)
	}
}

func TestSecretStoreCreateOpts(t *testing.T) {
	f := &secretStoreFlags{name: "n", secretType: "opaque", algorithm: "aes", bitLength: 256, mode: "cbc", payload: "hello"}
	opts, err := f.createOpts()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Payload != "aGVsbG8=" || opts.PayloadContentType != "application/octet-stream" || opts.PayloadContentEncoding != "base64" {
		t.Errorf("bytes payload: %+v", opts)
	}

	f.payloadContentType = "text/plain"
	if opts, _ = f.createOpts(); opts.Payload != "hello" || opts.PayloadContentType != "text/plain" || opts.PayloadContentEncoding != "" {
		t.Errorf("explicit content type: %+v", opts)
	}

	f = &secretStoreFlags{secretType: "opaque", expiration: "2030-01-02T15:04:05+03:00"}
	if opts, _ = f.createOpts(); opts.Payload != "" || opts.PayloadContentType != "" || opts.Expiration.Hour() != 12 {
		t.Errorf("no payload / UTC expiration: %+v", opts)
	}

	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&secretStoreFlags{file: empty}).createOpts(); err == nil {
		t.Error("an empty --file was accepted")
	}
	if _, err := (&secretStoreFlags{expiration: "tomorrow"}).createOpts(); err == nil {
		t.Error("a bad --expiration was accepted")
	}
}

func TestRunSecretStore(t *testing.T) {
	fs := th.SetupHTTP()
	defer fs.Teardown()
	var posts, gets []request
	fs.Mux.HandleFunc("/v1/secrets", record(t, &posts, http.StatusCreated, `{"secret_ref": "`+secretHref+`"}`))
	fs.Mux.HandleFunc("/v1/secrets/"+secretID, record(t, &gets, http.StatusOK, secretBody))

	f := &secretStoreFlags{name: "db-password", secretType: "opaque", algorithm: "aes", bitLength: 256, mode: "cbc", payload: "hello"}
	opts, err := f.createOpts()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := runSecretStore(context.Background(), kmClient(fs), jsonOut(), opts, &buf); err != nil {
		t.Fatal(err)
	}
	if posts[0].method != http.MethodPost || posts[0].apiVersion != "key-manager 1.1" {
		t.Errorf("POST = %+v", posts[0])
	}
	th.CheckJSONEquals(t, `{"name": "db-password", "secret_type": "opaque", "algorithm": "aes", "bit_length": 256,
		"mode": "cbc", "payload": "aGVsbG8=", "payload_content_type": "application/octet-stream",
		"payload_content_encoding": "base64"}`, json.RawMessage(posts[0].body))
	if len(gets) != 1 || !strings.Contains(buf.String(), `"Secret href": "`+secretHref+`"`) {
		t.Errorf("store did not show the stored secret: %s", buf.String())
	}
}

func TestRunSecretUpdate(t *testing.T) {
	fs := th.SetupHTTP()
	defer fs.Teardown()
	var got []request
	fs.Mux.HandleFunc("/v1/secrets/"+secretID, record(t, &got, http.StatusNoContent, ""))
	if err := runSecretUpdate(context.Background(), kmClient(fs), secretHref, "late payload"); err != nil {
		t.Fatal(err)
	}
	if got[0].method != http.MethodPut || got[0].contentType != "text/plain" || got[0].body != "late payload" {
		t.Errorf("PUT = %+v", got[0])
	}
}

func TestRunSecretDelete(t *testing.T) {
	fs := th.SetupHTTP()
	defer fs.Teardown()
	withConsumer := strings.Replace(secretBody, `"consumers": []`,
		`"consumers": [{"service": "image", "resource_type": "image", "resource_id": "x"}]`, 1)
	var got []request
	reply := secretBody
	fs.Mux.HandleFunc("/v1/secrets/"+secretID, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, request{method: r.Method, apiVersion: r.Header.Get("OpenStack-API-Version")})
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	})
	ctx := context.Background()

	if err := runSecretDelete(ctx, kmClient(fs), secretHref, false); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].method != http.MethodDelete || got[1].apiVersion != "key-manager 1.1" {
		t.Errorf("requests = %+v", got)
	}

	got, reply = nil, withConsumer
	if err := runSecretDelete(ctx, kmClient(fs), secretHref, false); !errors.Is(err, errSecretHasConsumers) {
		t.Errorf("err = %v, want errSecretHasConsumers", err)
	}
	if len(got) != 1 {
		t.Errorf("a refused delete sent %d requests, want only the GET", len(got))
	}

	got = nil
	if err := runSecretDelete(ctx, kmClient(fs), secretHref, true); err != nil || len(got) != 2 {
		t.Errorf("--force: err = %v, requests = %+v", err, got)
	}
}

func TestRunConsumerWriteAndList(t *testing.T) {
	fs := th.SetupHTTP()
	defer fs.Teardown()
	var got []request
	fs.Mux.HandleFunc("/v1/secrets/"+secretID+"/consumers", func(w http.ResponseWriter, r *http.Request) {
		record(t, &got, http.StatusOK, `{"consumers": [{"service": "image", "resource_type": "image",
			"resource_id": "abc", "created": "2026-01-02T03:04:05"}], "total": 1}`)(w, r)
	})
	ctx := context.Background()
	f := &consumerFlags{service: "image", resourceType: "image", resourceID: "abc"}
	want := `{"service": "image", "resource_type": "image", "resource_id": "abc"}`

	if err := runConsumerWrite(ctx, kmClient(fs), http.MethodPost, secretHref, f); err != nil {
		t.Fatal(err)
	}
	if err := runConsumerWrite(ctx, kmClient(fs), http.MethodDelete, secretHref, f); err != nil {
		t.Fatal(err)
	}
	for i, m := range []string{http.MethodPost, http.MethodDelete} {
		if got[i].method != m || got[i].apiVersion != "key-manager 1.1" {
			t.Errorf("request %d = %+v", i, got[i])
		}
		th.CheckJSONEquals(t, want, json.RawMessage(got[i].body))
	}

	var buf bytes.Buffer
	if err := runConsumerList(ctx, kmClient(fs), jsonOut(), secretID, 5, 2, &buf); err != nil {
		t.Fatal(err)
	}
	if got[2].query != "limit=5&offset=2" {
		t.Errorf("query = %q", got[2].query)
	}
	if !strings.Contains(buf.String(), `"Resource id": "abc"`) {
		t.Errorf("output = %s", buf.String())
	}
}

const rsaContainerBody = `{
  "container_ref": "` + contHref + `",
  "name": "keypair",
  "created": "2026-01-02T03:04:05",
  "status": "ACTIVE",
  "type": "rsa",
  "secret_refs": [
    {"name": "private_key", "secret_ref": "https://cloud.example.com/key-manager/v1/secrets/aaaa"},
    {"name": "public_key", "secret_ref": "https://cloud.example.com/key-manager/v1/secrets/bbbb"}
  ],
  "consumers": [{"name": "lb", "URL": "https://lb.example.com/1"}]
}`

func TestRunContainerListAndGet(t *testing.T) {
	fs := th.SetupHTTP()
	defer fs.Teardown()
	var got []request
	fs.Mux.HandleFunc("/v1/containers", record(t, &got, http.StatusOK, `{"containers": [`+rsaContainerBody+`], "total": 1}`))
	fs.Mux.HandleFunc("/v1/containers/"+containerID, record(t, &got, http.StatusOK, rsaContainerBody))
	ctx := context.Background()

	var buf bytes.Buffer
	if err := runContainerList(ctx, kmClient(fs), jsonOut(), containerListOpts{Limit: 10, Type: "rsa"}, &buf); err != nil {
		t.Fatal(err)
	}
	if got[0].query != "limit=10&type=rsa" {
		t.Errorf("query = %q", got[0].query)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	wantSecrets := "private_key=https://cloud.example.com/key-manager/v1/secrets/aaaa\npublic_key=https://cloud.example.com/key-manager/v1/secrets/bbbb"
	if rows[0]["Secrets"] != wantSecrets || rows[0]["Consumers"] != "lb=https://lb.example.com/1" {
		t.Errorf("row = %v", rows[0])
	}

	buf.Reset()
	if err := runContainerGet(ctx, kmClient(fs), jsonOut(), contHref, &buf); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["Public Key"] != "https://cloud.example.com/key-manager/v1/secrets/bbbb" || m["PK Passphrase"] != nil {
		t.Errorf("rsa get = %v", m)
	}
	if _, ok := m["Secrets"]; ok {
		t.Errorf("rsa get carries the generic Secrets field: %v", m)
	}
}

func TestContainerCreateOpts(t *testing.T) {
	f := &containerCreateFlags{name: "kp", typ: "rsa", secrets: []string{
		"private_key=" + secretHref, "bogus=" + secretHref, "no-equals", "public_key=" + secretHref,
	}}
	opts, err := f.createOpts()
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.SecretRefs) != 2 || opts.SecretRefs[0].Name != "private_key" || opts.SecretRefs[1].Name != "public_key" {
		t.Errorf("rsa secrets = %+v", opts.SecretRefs)
	}
	if _, err := (&containerCreateFlags{typ: "generic"}).createOpts(); err == nil {
		t.Error("no --secret was accepted")
	}
	if _, err := (&containerCreateFlags{typ: "magic", secrets: []string{"a=b"}}).createOpts(); err == nil {
		t.Error("an unknown --type was accepted")
	}
}

func TestRunContainerCreateAndDelete(t *testing.T) {
	fs := th.SetupHTTP()
	defer fs.Teardown()
	var posts, item []request
	fs.Mux.HandleFunc("/v1/containers", record(t, &posts, http.StatusCreated, `{"container_ref": "`+contHref+`"}`))
	fs.Mux.HandleFunc("/v1/containers/"+containerID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			record(t, &item, http.StatusNoContent, "")(w, r)
			return
		}
		record(t, &item, http.StatusOK, rsaContainerBody)(w, r)
	})
	ctx := context.Background()
	opts, err := (&containerCreateFlags{name: "g", typ: "generic", secrets: []string{"a=" + secretHref}}).createOpts()
	if err != nil {
		t.Fatal(err)
	}
	if err := runContainerCreate(ctx, kmClient(fs), jsonOut(), opts, io.Discard); err != nil {
		t.Fatal(err)
	}
	th.CheckJSONEquals(t, `{"name": "g", "type": "generic", "secret_refs": [{"name": "a", "secret_ref": "`+secretHref+`"}]}`,
		json.RawMessage(posts[0].body))
	if err := runContainerDelete(ctx, kmClient(fs), containerID); err != nil {
		t.Fatal(err)
	}
	if item[len(item)-1].method != http.MethodDelete {
		t.Errorf("last request = %+v", item[len(item)-1])
	}
}

func TestRunOrderCreateGetListDelete(t *testing.T) {
	fs := th.SetupHTTP()
	defer fs.Teardown()
	orderHref := "https://cloud.example.com/key-manager/v1/orders/" + orderID
	orderBody := `{"order_ref": "` + orderHref + `", "type": "key", "status": "ACTIVE",
		"secret_ref": "` + secretHref + `", "created": "2026-01-02T03:04:05",
		"meta": {"algorithm": "aes", "bit_length": 256, "mode": "cbc", "name": "k"}}`
	var list, item []request
	fs.Mux.HandleFunc("/v1/orders", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			record(t, &list, http.StatusAccepted, `{"order_ref": "`+orderHref+`"}`)(w, r)
			return
		}
		record(t, &list, http.StatusOK, `{"orders": [`+orderBody+`], "total": 1}`)(w, r)
	})
	fs.Mux.HandleFunc("/v1/orders/"+orderID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			record(t, &item, http.StatusNoContent, "")(w, r)
			return
		}
		record(t, &item, http.StatusOK, orderBody)(w, r)
	})
	ctx := context.Background()

	f := &orderCreateFlags{name: "k", algorithm: "aes", bitLength: 256, mode: "cbc", payloadContentType: "application/octet-stream"}
	opts, err := f.createOpts("key")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := runOrderCreate(ctx, kmClient(fs), jsonOut(), opts, &buf); err != nil {
		t.Fatal(err)
	}
	th.CheckJSONEquals(t, `{"type": "key", "meta": {"name": "k", "algorithm": "aes", "bit_length": 256, "mode": "cbc",
		"payload_content_type": "application/octet-stream"}}`, json.RawMessage(list[0].body))
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["Type"] != "Key" || m["Container href"] != "N/A" || m["Secret href"] != secretHref || m["Error code"] != nil {
		t.Errorf("order = %v", m)
	}

	buf.Reset()
	if err := runOrderList(ctx, kmClient(fs), jsonOut(), orders.ListOpts{Limit: 10}, &buf); err != nil {
		t.Fatal(err)
	}
	if list[1].query != "limit=10" || !strings.Contains(buf.String(), orderHref) {
		t.Errorf("list query %q, output %s", list[1].query, buf.String())
	}
	if err := runOrderGet(ctx, kmClient(fs), jsonOut(), orderHref, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := runOrderDelete(ctx, kmClient(fs), orderHref); err != nil {
		t.Fatal(err)
	}
	if item[len(item)-1].method != http.MethodDelete {
		t.Errorf("last request = %+v", item[len(item)-1])
	}
}

func TestParseACLRef(t *testing.T) {
	e, err := parseACLRef(contHref + "/")
	if err != nil || !e.container || e.id != containerID || e.ref != contHref {
		t.Errorf("container href: %+v, %v", e, err)
	}
	if _, err := parseACLRef(secretID); err == nil {
		t.Error("a bare UUID was accepted")
	}
}

func TestACLChanges(t *testing.T) {
	server := aclOf(`{"read": {"project-access": true, "users": ["u1", "u2"]}}`, t)
	yes, no := true, false

	got := aclAddUsers(server, &aclFlags{users: []string{"u2", "u3"}, operationType: "read", projectAccess: &no})
	th.CheckJSONEquals(t, `{"read": {"users": ["u1", "u2", "u3"], "project-access": false}}`, setMap(t, got))

	got = aclRemoveUsers(server, &aclFlags{users: []string{"u1", "zz"}, operationType: "read"})
	th.CheckJSONEquals(t, `{"read": {"users": ["u2"], "project-access": true}}`, setMap(t, got))

	got = aclSubmit(server, &aclFlags{operationType: "read", projectAccess: &yes})
	th.CheckJSONEquals(t, `{"read": {"users": [], "project-access": true}}`, setMap(t, got))

	got = aclSubmit(nil, &aclFlags{users: []string{"u9"}, operationType: "read"})
	th.CheckJSONEquals(t, `{"read": {"users": ["u9"]}}`, setMap(t, got))

	got = aclAddUsers(aclOf(`{}`, t), &aclFlags{users: []string{"u1"}, operationType: "read"})
	th.CheckJSONEquals(t, `{"read": {"users": ["u1"]}}`, setMap(t, got))
}

func TestRunACLWriteAndDelete(t *testing.T) {
	fs := th.SetupHTTP()
	defer fs.Teardown()
	var got []request
	fs.Mux.HandleFunc("/v1/secrets/"+secretID+"/acl", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			record(t, &got, http.StatusOK, `{"acl_ref": "`+secretHref+`/acl"}`)(w, r)
		case http.MethodDelete:
			record(t, &got, http.StatusOK, "")(w, r)
		default:
			record(t, &got, http.StatusOK, `{"read": {"project-access": false, "users": ["u1"],
				"created": "2026-01-02T03:04:05", "updated": "2026-01-02T03:04:05"}}`)(w, r)
		}
	})
	ctx := context.Background()
	e, err := parseACLRef(secretHref)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	verb := aclVerb{aclAddUsers, true}
	if err := runACLWrite(ctx, kmClient(fs), jsonOut(), e, &aclFlags{users: []string{"u2"}, operationType: "read"}, verb, &buf); err != nil {
		t.Fatal(err)
	}
	if methods := []string{got[0].method, got[1].method, got[2].method}; methods[0] != http.MethodGet ||
		methods[1] != http.MethodPut || methods[2] != http.MethodGet {
		t.Errorf("methods = %v, want GET PUT GET", methods)
	}
	th.CheckJSONEquals(t, `{"read": {"users": ["u1", "u2"], "project-access": false}}`, json.RawMessage(got[1].body))
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0]["Operation Type"] != "read" || rows[0]["Secret ACL Ref"] != secretHref+"/acl" || rows[0]["Project Access"] != false {
		t.Errorf("rows = %v", rows)
	}

	got = nil
	if err := runACLWrite(ctx, kmClient(fs), jsonOut(), e, &aclFlags{operationType: "read"}, aclVerb{change: aclSubmit}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got[0].method != http.MethodPut {
		t.Errorf("submit read the ACL first: %+v", got)
	}
	if err := runACLWrite(ctx, kmClient(fs), jsonOut(), e, &aclFlags{operationType: "write"}, aclVerb{change: aclSubmit}, io.Discard); err == nil {
		t.Error("--operation-type write was accepted")
	}

	got = nil
	if err := runACLDelete(ctx, kmClient(fs), e); err != nil {
		t.Fatal(err)
	}
	if got[0].method != http.MethodDelete {
		t.Errorf("delete = %+v", got[0])
	}
}

func aclOf(s string, t *testing.T) acls.ACL {
	t.Helper()
	var m acls.ACL
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func setMap(t *testing.T, opts interface {
	ToACLSetMap() (map[string]any, error)
},
) json.RawMessage {
	t.Helper()
	m, err := opts.ToACLSetMap()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
