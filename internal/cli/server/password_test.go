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
	"golang.org/x/crypto/ssh"

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
	return writeKey(t, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
}

func writeKey(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing the test key: %v", err)
	}
	return path
}

// opensshKey encodes key the way ssh-keygen has by default since OpenSSH 7.8.
func opensshKey(t *testing.T, key *rsa.PrivateKey, passphrase string) []byte {
	t.Helper()
	var block *pem.Block
	var err error
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(key, "koc@example.com")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(key, "koc@example.com", []byte(passphrase))
	}
	if err != nil {
		t.Fatalf("marshalling an OpenSSH key: %v", err)
	}
	return pem.EncodeToMemory(block)
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

func TestRunServerPasswordShow_DecryptsWithAnOpenSSHKey(t *testing.T) {
	// The ssh-keygen default since OpenSSH 7.8, so the key an operator already
	// has on disk — no "convert it to PEM first" detour.
	key, ciphertext := newTestKeypair(t, "ssh-format-Pass")
	path := writeKey(t, opensshKey(t, key, ""))

	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handlePasswordGet(t, fakeServer, ciphertext)

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runServerPasswordShow(context.Background(), computeClient(fakeServer, ""), o,
		passwordServerID, path, &out); err != nil {
		t.Fatalf("runServerPasswordShow returned error: %v", err)
	}
	th.AssertEquals(t, "ssh-format-Pass", strings.TrimSpace(out.String()))
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
		{"not pem", "ssh-rsa AAAAB3NzaC1yc2E= user@host\n", "not PEM-encoded"},
		{"truncated", "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n", "parsing the private key"},
	} {
		_, err := parseRSAPrivateKey([]byte(tc.pem), nil)
		if err == nil {
			t.Errorf("%s: an unusable key was accepted", tc.name)
			continue
		}
		// Each unusable input gets its own message; "invalid key" would leave the
		// operator guessing which problem they have.
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}

func TestParseRSAPrivateKey_UnlocksProtectedKeysWithThePassphrase(t *testing.T) {
	key, _ := newTestKeypair(t, "unused")
	legacy, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", //nolint:staticcheck // SA1019: the format ssh-keygen -m PEM still writes
		x509.MarshalPKCS1PrivateKey(key), []byte("hunter2"), x509.PEMCipherAES256)
	if err != nil {
		t.Fatalf("encrypting a legacy PEM key: %v", err)
	}

	// Both protected encodings an operator can have: the OpenSSH default and the
	// legacy "openssl-style" PEM.
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"openssh", opensshKey(t, key, "hunter2")},
		{"legacy pem", pem.EncodeToMemory(legacy)},
	} {
		asked := 0
		parsed, err := parseRSAPrivateKey(tc.raw, func() ([]byte, error) {
			asked++
			return []byte("hunter2"), nil
		})
		if err != nil {
			t.Errorf("%s: parseRSAPrivateKey returned error: %v", tc.name, err)
			continue
		}
		if asked != 1 {
			t.Errorf("%s: the passphrase was asked for %d times, want 1", tc.name, asked)
		}
		if !parsed.Equal(key) {
			t.Errorf("%s: the decrypted key differs from the original", tc.name)
		}
	}
}

func TestParseRSAPrivateKey_PlainKeyIsNotAskedAbout(t *testing.T) {
	key, _ := newTestKeypair(t, "unused")
	// A prompt on an unprotected key would look like koc doubting the key.
	if _, err := parseRSAPrivateKey(opensshKey(t, key, ""), func() ([]byte, error) {
		t.Errorf("a passphrase was requested for an unprotected key")
		return nil, nil
	}); err != nil {
		t.Fatalf("parseRSAPrivateKey returned error: %v", err)
	}
}

func TestLoadRSAPrivateKey_TakesTheRedirectedPassphrase(t *testing.T) {
	key, _ := newTestKeypair(t, "unused")
	keyPath := writeKey(t, opensshKey(t, key, "hunter2"))

	// The non-interactive form, "... --private-key <key> < passphrase": stdin is
	// a file rather than a terminal, so the passphrase comes from it and nothing
	// blocks waiting for a prompt.
	stdin, err := os.Open(writeKey(t, []byte("hunter2\n")))
	if err != nil {
		t.Fatalf("opening the passphrase file: %v", err)
	}
	defer func() { _ = stdin.Close() }()
	saved := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() { os.Stdin = saved })

	loaded, err := loadRSAPrivateKey(keyPath)
	if err != nil {
		t.Fatalf("loadRSAPrivateKey returned error: %v", err)
	}
	if !loaded.Equal(key) {
		t.Errorf("the decrypted key differs from the original")
	}
}

func TestReadRedirectedPassphrase(t *testing.T) {
	for _, tc := range []struct {
		name, stdin, want string
	}{
		{"a file with a trailing newline", "hunter2\n", "hunter2"},
		{"crlf", "hunter2\r\n", "hunter2"},
		{"no line ending", "hunter2", "hunter2"},
		// Only the line ending is trimmed: a passphrase may contain — and end
		// in — a space, and the key file below it must not be swallowed either.
		{"spaces kept", "two words \n", "two words "},
		{"first line only", "hunter2\nnot the passphrase\n", "hunter2"},
	} {
		got, err := readRedirectedPassphrase(strings.NewReader(tc.stdin))
		if err != nil {
			t.Errorf("%s: readRedirectedPassphrase returned error: %v", tc.name, err)
			continue
		}
		th.AssertEquals(t, tc.want, string(got))
	}
}

func TestReadRedirectedPassphrase_ExplainsAnEmptyStdin(t *testing.T) {
	// The CI shape: stdin is /dev/null, so blocking on a prompt is impossible
	// and "EOF" would not say what to do.
	_, err := readRedirectedPassphrase(strings.NewReader(""))
	if err == nil {
		t.Fatalf("an empty stdin was accepted as a passphrase")
	}
	if !strings.Contains(err.Error(), "redirect it in") {
		t.Errorf("error %q does not say how to supply the passphrase", err)
	}
}

func TestParseRSAPrivateKey_ExplainsAProtectedKeyWithNoTerminal(t *testing.T) {
	key, _ := newTestKeypair(t, "unused")
	// Non-interactively there is nothing to prompt on, so say what to do
	// instead of failing with ssh's "key is passphrase protected".
	_, err := parseRSAPrivateKey(opensshKey(t, key, "hunter2"), nil)
	if err == nil {
		t.Fatalf("a protected key was accepted with no way to ask for the passphrase")
	}
	if !strings.Contains(err.Error(), "ssh-keygen -p") {
		t.Errorf("error %q does not say how to strip the passphrase", err)
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
	_, err = parseRSAPrivateKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil)
	if err == nil {
		t.Fatalf("an ed25519 key was accepted")
	}
	if !strings.Contains(err.Error(), "RSA") {
		t.Errorf("error %q does not say the key must be RSA", err)
	}
}
