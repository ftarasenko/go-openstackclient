package keymanager

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// execKM runs argv through the real command tree (Execute → RunE → seam) with
// the key-manager endpoint pointed at fs.
func execKM(t *testing.T, fs th.FakeServer, argv ...string) (string, error) {
	t.Helper()
	a := &auth.Options{}
	provider := &gophercloud.ProviderClient{
		TokenID: "fake-token",
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) {
			return fs.Server.URL + "/", nil
		},
	}
	a.SetAuthenticatorForTest(func(context.Context) (*auth.Client, error) {
		return a.NewClientForTest(provider, gophercloud.EndpointOpts{}), nil
	})
	root := &cobra.Command{Use: "koc"}
	root.AddCommand(NewCommand(a, &output.Options{Format: "value"})...)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(argv)
	root.SilenceUsage = true
	root.SilenceErrors = true
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

// fakeBarbican answers every route the package uses with a canned document and
// records "METHOD path" per request, plus the microversion header it saw.
func fakeBarbican(t *testing.T) (th.FakeServer, *[]string) {
	t.Helper()
	fs := th.SetupHTTP()
	var seen []string
	reply := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, r.Method+" "+r.URL.Path)
			if v := r.Header.Get("OpenStack-API-Version"); v != "key-manager 1.1" {
				t.Errorf("%s %s: OpenStack-API-Version = %q", r.Method, r.URL.Path, v)
			}
			if body != "" {
				w.Header().Set("Content-Type", "application/json")
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	byMethod := func(m map[string]http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { m[r.Method](w, r) }
	}
	orderBody := `{"order_ref": "https://cloud.example.com/v1/orders/` + orderID + `", "type": "asymmetric",
		"status": "PENDING", "container_ref": "` + contHref + `"}`
	fs.Mux.HandleFunc("/v1/secrets", byMethod(map[string]http.HandlerFunc{
		http.MethodGet:  reply(http.StatusOK, `{"secrets": [`+secretBody+`]}`),
		http.MethodPost: reply(http.StatusCreated, `{"secret_ref": "`+secretHref+`"}`),
	}))
	fs.Mux.HandleFunc("/v1/secrets/"+secretID, byMethod(map[string]http.HandlerFunc{
		http.MethodGet:    reply(http.StatusOK, secretBody),
		http.MethodPut:    reply(http.StatusNoContent, ""),
		http.MethodDelete: reply(http.StatusNoContent, ""),
	}))
	fs.Mux.HandleFunc("/v1/secrets/"+secretID+"/payload", reply(http.StatusOK, "s3cr3t"))
	fs.Mux.HandleFunc("/v1/secrets/"+secretID+"/consumers", byMethod(map[string]http.HandlerFunc{
		http.MethodGet:    reply(http.StatusOK, `{"consumers": []}`),
		http.MethodPost:   reply(http.StatusOK, secretBody),
		http.MethodDelete: reply(http.StatusOK, secretBody),
	}))
	fs.Mux.HandleFunc("/v1/secrets/"+secretID+"/acl", byMethod(map[string]http.HandlerFunc{
		http.MethodGet:    reply(http.StatusOK, `{"read": {"project-access": true}}`),
		http.MethodPut:    reply(http.StatusOK, `{"acl_ref": "`+secretHref+`/acl"}`),
		http.MethodDelete: reply(http.StatusOK, ""),
	}))
	fs.Mux.HandleFunc("/v1/containers", byMethod(map[string]http.HandlerFunc{
		http.MethodGet:  reply(http.StatusOK, `{"containers": [`+rsaContainerBody+`]}`),
		http.MethodPost: reply(http.StatusCreated, `{"container_ref": "`+contHref+`"}`),
	}))
	fs.Mux.HandleFunc("/v1/containers/"+containerID, byMethod(map[string]http.HandlerFunc{
		http.MethodGet:    reply(http.StatusOK, rsaContainerBody),
		http.MethodDelete: reply(http.StatusNoContent, ""),
	}))
	fs.Mux.HandleFunc("/v1/containers/"+containerID+"/acl", byMethod(map[string]http.HandlerFunc{
		http.MethodGet:    reply(http.StatusOK, `{"read": {"project-access": true}}`),
		http.MethodPut:    reply(http.StatusOK, `{"acl_ref": "`+contHref+`/acl"}`),
		http.MethodDelete: reply(http.StatusOK, ""),
	}))
	fs.Mux.HandleFunc("/v1/orders", byMethod(map[string]http.HandlerFunc{
		http.MethodGet:  reply(http.StatusOK, `{"orders": [`+orderBody+`]}`),
		http.MethodPost: reply(http.StatusAccepted, `{"order_ref": "https://cloud.example.com/v1/orders/`+orderID+`"}`),
	}))
	fs.Mux.HandleFunc("/v1/orders/"+orderID, byMethod(map[string]http.HandlerFunc{
		http.MethodGet:    reply(http.StatusOK, orderBody),
		http.MethodDelete: reply(http.StatusNoContent, ""),
	}))
	return fs, &seen
}

func TestExec_EveryLeaf(t *testing.T) {
	fs, seen := fakeBarbican(t)
	defer fs.Teardown()
	payload := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(payload, []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		argv []string
		last string // the last request the verb must have sent
		out  string // a substring of the output, if any
	}{
		{[]string{"secret", "list", "--name", "db-password"}, "GET /v1/secrets", secretHref},
		{[]string{"secret", "get", secretHref}, "GET /v1/secrets/" + secretID, "db-password"},
		{[]string{"secret", "get", secretID, "--payload_content_type", "text/plain", "-p"}, "GET /v1/secrets/" + secretID + "/payload", "s3cr3t"},
		{[]string{"secret", "store", "-n", "db-password", "-F", payload, "-x", "2030-01-01"}, "GET /v1/secrets/" + secretID, "ACTIVE"},
		{[]string{"secret", "update", secretHref, "late"}, "PUT /v1/secrets/" + secretID, ""},
		{[]string{"secret", "delete", secretHref, "--force"}, "DELETE /v1/secrets/" + secretID, ""},
		{[]string{"secret", "consumer", "create", secretHref, "-s", "image", "-t", "image", "-i", "abc"}, "POST /v1/secrets/" + secretID + "/consumers", ""},
		{[]string{"secret", "consumer", "delete", secretHref, "-s", "image", "-t", "image", "-i", "abc"}, "DELETE /v1/secrets/" + secretID + "/consumers", ""},
		{[]string{"secret", "consumer", "list", secretHref}, "GET /v1/secrets/" + secretID + "/consumers", ""},
		{[]string{"secret", "container", "list", "--type", "rsa"}, "GET /v1/containers", contHref},
		{[]string{"secret", "container", "get", contHref}, "GET /v1/containers/" + containerID, "keypair"},
		{[]string{"secret", "container", "create", "--type", "rsa", "-s", "private_key=" + secretHref}, "GET /v1/containers/" + containerID, "rsa"},
		{[]string{"secret", "container", "delete", contHref}, "DELETE /v1/containers/" + containerID, ""},
		{[]string{"secret", "order", "list"}, "GET /v1/orders", "Asymmetric"},
		{[]string{"secret", "order", "get", orderID}, "GET /v1/orders/" + orderID, "PENDING"},
		{[]string{"secret", "order", "create", "asymmetric", "-a", "rsa", "-b", "2048"}, "GET /v1/orders/" + orderID, "N/A"},
		{[]string{"secret", "order", "delete", orderID}, "DELETE /v1/orders/" + orderID, ""},
		{[]string{"acl", "get", secretHref}, "GET /v1/secrets/" + secretID + "/acl", secretHref + "/acl"},
		{[]string{"acl", "submit", contHref, "-u", "u1", "--no-project-access"}, "GET /v1/containers/" + containerID + "/acl", contHref + "/acl"},
		{[]string{"acl", "user", "add", secretHref, "-u", "u1"}, "GET /v1/secrets/" + secretID + "/acl", ""},
		{[]string{"acl", "user", "remove", secretHref, "-u", "u1", "--project-access"}, "GET /v1/secrets/" + secretID + "/acl", ""},
		{[]string{"acl", "delete", contHref}, "DELETE /v1/containers/" + containerID + "/acl", ""},
	} {
		t.Run(strings.Join(tc.argv[:3], " "), func(t *testing.T) {
			*seen = nil
			out, err := execKM(t, fs, tc.argv...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if len(*seen) == 0 || (*seen)[len(*seen)-1] != tc.last {
				t.Errorf("requests = %v, want the last to be %q", *seen, tc.last)
			}
			if !strings.Contains(out, tc.out) {
				t.Errorf("output lacks %q:\n%s", tc.out, out)
			}
		})
	}
}

func TestExec_Refusals(t *testing.T) {
	fs, seen := fakeBarbican(t)
	defer fs.Teardown()
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"secret", "get", "not-a-ref"}, "Secret incorrectly specified"},
		{[]string{"secret", "get", secretID, "-p", "-d"}, "none of the others can be"},
		{[]string{"secret", "order", "create", "certificate"}, "invalid argument"},
		{[]string{"secret", "container", "create"}, "at least one --secret"},
		{[]string{"secret", "consumer", "create", secretID}, "required flag"},
		{[]string{"acl", "get", secretID}, "secret or container URI is not specified"},
		{[]string{"acl", "submit", secretHref, "--project-access", "--no-project-access"}, "none of the others can be"},
		{[]string{"secret", "store", "-x", "soon"}, "--expiration"},
	} {
		t.Run(strings.Join(tc.argv, " "), func(t *testing.T) {
			*seen = nil
			out, err := execKM(t, fs, tc.argv...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q\n%s", err, tc.want, out)
			}
			if len(*seen) != 0 {
				t.Errorf("a refused invocation sent %v", *seen)
			}
		})
	}
}
