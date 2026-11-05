package keymanager_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/secrets"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/keymanager"
)

// session points an openstack.Session at a fake barbican.
func session(h http.HandlerFunc) (*openstack.Session, func()) {
	srv := httptest.NewServer(h)
	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return srv.URL + "/", nil },
	}
	return openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{}), srv.Close
}

// Storing a secret returns its UUID, which every other call takes.
func ExampleCreateSecret() {
	sess, done := session(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"secret_ref":"https://kms.example.com/v1/secrets/11111111-2222-3333-4444-555555555555"}`)
			return
		}
		_, _ = io.WriteString(w, `{"name":"db-password","status":"ACTIVE"}`)
	})
	defer done()
	barbican, err := sess.KeyManager()
	if err != nil {
		fmt.Println(err)
		return
	}

	ctx := context.Background()
	id, err := keymanager.CreateSecret(ctx, barbican, secrets.CreateOpts{
		Name: "db-password", SecretType: secrets.PassphraseSecret,
		Payload: "example", PayloadContentType: "text/plain",
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	s, err := keymanager.GetSecret(ctx, barbican, id)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(id, s.Name, s.Status)
	// Output:
	// 11111111-2222-3333-4444-555555555555 db-password ACTIVE
}

// An ACL is addressed by the secret's or container's href, and AddACLUsers
// keeps the users already on it.
func ExampleAddACLUsers() {
	sess, done := session(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			fmt.Println("wrote the read ACL")
			_, _ = io.WriteString(w, `{"acl_ref":"https://kms.example.com/v1/containers/66666666-7777-8888-9999-aaaaaaaaaaaa/acl"}`)
			return
		}
		_, _ = io.WriteString(w, `{"read":{"project-access":true,"users":["alice"]}}`)
	})
	defer done()
	barbican, err := sess.KeyManager()
	if err != nil {
		fmt.Println(err)
		return
	}

	target, err := keymanager.ParseACLRef("https://kms.example.com/v1/containers/66666666-7777-8888-9999-aaaaaaaaaaaa")
	if err != nil {
		fmt.Println(err)
		return
	}
	noProject := false
	if err := keymanager.AddACLUsers(context.Background(), barbican, target, keymanager.ACLChange{
		OperationType: "read", Users: []string{"bob"}, ProjectAccess: &noProject,
	}); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("container", target.ID, "is readable by its users only")
	// Output:
	// wrote the read ACL
	// container 66666666-7777-8888-9999-aaaaaaaaaaaa is readable by its users only
}
