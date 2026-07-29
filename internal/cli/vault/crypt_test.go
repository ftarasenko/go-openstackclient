package vaultcli

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testKey is generated once: 2048-bit RSA keygen is slow enough to matter across
// a dozen test cases.
var testKey = func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}()

func TestEncryptDecryptPayload(t *testing.T) {
	const aad = "deployments/itkey/dev/openrc"
	plaintext := []byte(`{"OS_PASSWORD":"s3cret"}`)

	envelope, err := encryptPayload(&testKey.PublicKey, aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(envelope, envelopeHeader+"\n") {
		t.Errorf("envelope does not start with the version header:\n%s", envelope)
	}
	if strings.Contains(envelope, "s3cret") || strings.Contains(envelope, "OS_PASSWORD") {
		t.Errorf("envelope leaked plaintext:\n%s", envelope)
	}

	got, err := decryptPayload(testKey, aad, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("round trip = %q, want %q", got, plaintext)
	}

	// Two encryptions of the same input must differ (fresh key and nonce).
	again, err := encryptPayload(&testKey.PublicKey, aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if again == envelope {
		t.Error("two encryptions produced identical ciphertext")
	}
}

func TestDecryptPayload_Rejections(t *testing.T) {
	const aad = "a/b"
	envelope, err := encryptPayload(&testKey.PublicKey, aad, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("wrong path fails authentication", func(t *testing.T) {
		if _, err := decryptPayload(testKey, "other/path", envelope); err == nil {
			t.Error("a payload must not decrypt under a different path")
		}
	})

	t.Run("wrong identity fails", func(t *testing.T) {
		other, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decryptPayload(other, aad, envelope); err == nil {
			t.Error("a payload must not decrypt with an unrelated key")
		}
	})

	t.Run("tampered ciphertext fails", func(t *testing.T) {
		bad := envelope[:len(envelope)-4] + "AAAA"
		if _, err := decryptPayload(testKey, aad, bad); err == nil {
			t.Error("a tampered payload must not decrypt")
		}
	})

	t.Run("foreign format is rejected", func(t *testing.T) {
		if _, err := decryptPayload(testKey, aad, "not-an-envelope"); err == nil {
			t.Error("a payload without a header must be rejected")
		}
		if _, err := decryptPayload(testKey, aad, "koc-enc:v9:rot13\nAAAA"); err == nil {
			t.Error("an unknown envelope version must be rejected")
		}
	})
}

func TestLoadRecipientAndIdentity(t *testing.T) {
	dir := t.TempDir()
	write := func(name, blockType string, der []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	pkix, err := x509.MarshalPKIXPublicKey(&testKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(testKey)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, blockType string
		der             []byte
	}{
		{"pkix.pub", "PUBLIC KEY", pkix},
		{"pkcs1.pub", "RSA PUBLIC KEY", x509.MarshalPKCS1PublicKey(&testKey.PublicKey)},
	} {
		if _, err := loadRecipient(write(c.name, c.blockType, c.der)); err != nil {
			t.Errorf("loadRecipient(%s) = %v", c.name, err)
		}
	}

	for _, c := range []struct {
		name, blockType string
		der             []byte
	}{
		{"pkcs8.key", "PRIVATE KEY", pkcs8},
		{"pkcs1.key", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(testKey)},
	} {
		if _, err := loadIdentity(write(c.name, c.blockType, c.der)); err != nil {
			t.Errorf("loadIdentity(%s) = %v", c.name, err)
		}
	}

	t.Run("a short key is refused", func(t *testing.T) {
		short, err := rsa.GenerateKey(rand.Reader, 1024)
		if err != nil {
			t.Fatal(err)
		}
		p := write("short.pub", "RSA PUBLIC KEY", x509.MarshalPKCS1PublicKey(&short.PublicKey))
		_, err = loadRecipient(p)
		if err == nil || !strings.Contains(err.Error(), "minimum") {
			t.Errorf("err = %v, want a key-size refusal", err)
		}
	})

	t.Run("a private key is not a recipient", func(t *testing.T) {
		if _, err := loadRecipient(write("priv.pem", "PRIVATE KEY", pkcs8)); err == nil {
			t.Error("a PRIVATE KEY block must not be accepted as --recipient")
		}
	})

	t.Run("an encrypted identity is refused with a hint", func(t *testing.T) {
		p := write("enc.key", "ENCRYPTED PRIVATE KEY", []byte("whatever"))
		_, err := loadIdentity(p)
		if err == nil || !strings.Contains(err.Error(), "openssl pkey") {
			t.Errorf("err = %v, want a hint to decrypt the key first", err)
		}
	})

	t.Run("non-PEM input is refused", func(t *testing.T) {
		p := filepath.Join(dir, "junk")
		if err := os.WriteFile(p, []byte("ssh-rsa AAAA..."), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadRecipient(p); err == nil {
			t.Error("non-PEM input must be refused")
		}
	})
}
