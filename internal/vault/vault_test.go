package vault

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadKVData_AppRoleLoginAndNamespace(t *testing.T) {
	var loginBody map[string]string
	var readToken, readNS string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/approle/login":
			_ = json.NewDecoder(r.Body).Decode(&loginBody)
			if r.Header.Get("X-Vault-Namespace") != "team-a" {
				t.Errorf("login namespace = %q", r.Header.Get("X-Vault-Namespace"))
			}
			_, _ = w.Write([]byte(`{"auth":{"client_token":"tok-123"}}`))
		case "/v1/secret_v2/data/deployments/x/openrc":
			readToken = r.Header.Get("X-Vault-Token")
			readNS = r.Header.Get("X-Vault-Namespace")
			_, _ = w.Write([]byte(`{"data":{"data":{"value":"export OS_USERNAME=admin\n"}}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := New(context.Background(), Config{
		Addr:      srv.URL,
		Namespace: "team-a",
		RoleID:    "rid",
		SecretID:  "sid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if loginBody["role_id"] != "rid" || loginBody["secret_id"] != "sid" {
		t.Errorf("login body = %+v", loginBody)
	}

	data, err := c.ReadKVData(context.Background(), "deployments/x/openrc")
	if err != nil {
		t.Fatal(err)
	}
	if data["value"] != "export OS_USERNAME=admin\n" {
		t.Errorf("value = %v", data["value"])
	}
	if readToken != "tok-123" {
		t.Errorf("read X-Vault-Token = %q, want tok-123", readToken)
	}
	if readNS != "team-a" {
		t.Errorf("read X-Vault-Namespace = %q", readNS)
	}
}

func TestNew_TokenSkipsLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/approle/login" {
			t.Fatal("AppRole login should be skipped when a token is supplied")
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"value":"x"}}}`))
	}))
	defer srv.Close()

	c, err := New(context.Background(), Config{Addr: srv.URL, Token: "preset"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadKVData(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
}

func TestNew_RequiresAuth(t *testing.T) {
	if _, err := New(context.Background(), Config{Addr: "https://v"}); err == nil {
		t.Fatal("expected error when neither token nor AppRole is provided")
	}
	if _, err := New(context.Background(), Config{}); err == nil {
		t.Fatal("expected error when address is missing")
	}
}

func TestReadKVData_VaultError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
	}))
	defer srv.Close()
	c, _ := New(context.Background(), Config{Addr: srv.URL, Token: "t"})
	_, err := c.ReadKVData(context.Background(), "p")
	if err == nil {
		t.Fatal("expected permission denied error")
	}
}
