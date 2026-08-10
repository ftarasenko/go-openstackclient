package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const passwordServerID = "5cb3a2c1-1111-2222-3333-444455556666"

// newTestKeypair returns an RSA key and the ciphertext nova would store for
// plaintext: base64 of the PKCS#1 v1.5 encryption under the public half. Going
// through the real primitives is the point — a hand-written fixture would pin
// the transport but prove nothing about the decryption path.
func newTestKeypair(t *testing.T, plaintext string) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating a test key: %v", err)
	}
	ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, &key.PublicKey, []byte(plaintext))
	if err != nil {
		t.Fatalf("encrypting the test password: %v", err)
	}
	return key, base64.StdEncoding.EncodeToString(ciphertext)
}

func writePEM(t *testing.T, blockType string, der []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing the test key: %v", err)
	}
	return path
}

// handlePasswordGet serves GET /servers/{id}/os-server-password plus the server
// lookup the name→ID resolver does first.
func handlePasswordGet(t *testing.T, fakeServer th.FakeServer, ciphertext string) {
	t.Helper()
	fakeServer.Mux.HandleFunc("/servers/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers": [{"ID": "` + passwordServerID + `", "name": "kocrev-vm"}]}`))
	})
	fakeServer.Mux.HandleFunc("/servers/"+passwordServerID+"/os-server-password",
		func(w http.ResponseWriter, r *http.Request) {
			th.AssertEquals(t, http.MethodGet, r.Method)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"password": "` + ciphertext + `"}`))
		})
}

func TestRunServerPasswordShow_DecryptsWithAPKCS1Key(t *testing.T) {
	key, ciphertext := newTestKeypair(t, "s3cret-Adm1n")
	path := writePEM(t, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))

	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handlePasswordGet(t, fakeServer, ciphertext)

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runServerPasswordShow(context.Background(), computeClient(fakeServer, ""), o,
		passwordServerID, path, &out)
	if err != nil {
		t.Fatalf("runServerPasswordShow returned error: %v", err)
	}
	th.AssertEquals(t, "s3cret-Adm1n", strings.TrimSpace(out.String()))
}

func TestRunServerPasswordShow_DecryptsWithAPKCS8Key(t *testing.T) {
	key, ciphertext := newTestKeypair(t, "another-Pass")
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling PKCS#8: %v", err)
	}
	path := writePEM(t, "PRIVATE KEY", der)

	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handlePasswordGet(t, fakeServer, ciphertext)

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runServerPasswordShow(context.Background(), computeClient(fakeServer, ""), o,
		passwordServerID, path, &out); err != nil {
		t.Fatalf("runServerPasswordShow returned error: %v", err)
	}
	th.AssertEquals(t, "another-Pass", strings.TrimSpace(out.String()))
}

func TestRunServerPasswordShow_WithoutAKeyPrintsTheCiphertext(t *testing.T) {
	_, ciphertext := newTestKeypair(t, "s3cret-Adm1n")

	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handlePasswordGet(t, fakeServer, ciphertext)

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runServerPasswordShow(context.Background(), computeClient(fakeServer, ""), o,
		passwordServerID, "", &out); err != nil {
		t.Fatalf("runServerPasswordShow returned error: %v", err)
	}
	// Without --private-key the base64 ciphertext passes through untouched, so
	// it can be decrypted elsewhere.
	th.AssertEquals(t, ciphertext, strings.TrimSpace(out.String()))
}

func TestRunServerPasswordShow_EmptyPasswordIsExplained(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handlePasswordGet(t, fakeServer, "")

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runServerPasswordShow(context.Background(), computeClient(fakeServer, ""), o,
		passwordServerID, "", &out)
	// Nova answers 200 with an empty string before the guest agent posts one;
	// rendering a blank row would read as "the password is empty".
	if err == nil {
		t.Fatalf("an empty stored password was reported as success")
	}
	if !strings.Contains(err.Error(), "no stored admin password") {
		t.Errorf("error does not explain the empty password: %v", err)
	}
}

func TestRunServerPasswordShow_BadKeyFailsBeforeTheAPICall(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("the API was called despite an unreadable key: %s %s", r.Method, r.URL.Path)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runServerPasswordShow(context.Background(), computeClient(fakeServer, ""), o,
		passwordServerID, filepath.Join(t.TempDir(), "missing.pem"), &out)
	if err == nil {
		t.Fatalf("a missing key file was accepted")
	}
}

func TestParseRSAPrivateKey_NamesTheFormatItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name, pem, want string
	}{
		{
			"openssh",
			"-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n",
			"ssh-keygen -p -m PEM",
		},
		{
			"encrypted",
			"-----BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-128-CBC,00\n\nAAAA\n-----END RSA PRIVATE KEY-----\n",
			"passphrase-protected",
		},
		{"not pem", "ssh-rsa AAAAB3NzaC1yc2E= user@host\n", "not PEM-encoded"},
	} {
		_, err := parseRSAPrivateKey([]byte(tc.pem))
		if err == nil {
			t.Errorf("%s: an unusable key was accepted", tc.name)
			continue
		}
		// Each unusable format gets its own message; "invalid key" would leave
		// the operator guessing which of three problems they have.
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}

func TestParseRSAPrivateKey_RejectsNonRSAKeys(t *testing.T) {
	// Nova encrypts with RSA PKCS#1 v1.5, so an ed25519 keypair cannot carry
	// this scheme at all — say that rather than failing at decrypt time.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating an ed25519 key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshalling PKCS#8: %v", err)
	}
	_, err = parseRSAPrivateKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err == nil {
		t.Fatalf("an ed25519 key was accepted")
	}
	if !strings.Contains(err.Error(), "RSA") {
		t.Errorf("error %q does not say the key must be RSA", err)
	}
}
