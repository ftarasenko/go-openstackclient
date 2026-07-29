package vaultcli

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

// The export envelope. Each payload is encrypted with a fresh AES-256-GCM key,
// which is itself wrapped to the recipient's RSA public key with OAEP-SHA256 —
// the classic hybrid scheme, using nothing outside the standard library so the
// air-gap / vendored-deps invariant holds.
//
// Wire format (the text placed in a JUnit <system-out>):
//
//	koc-enc:v1:rsa-oaep-sha256:aes-256-gcm
//	<base64: uint16be len(wrapped) | wrapped | nonce(12) | ciphertext+tag>
//
// The secret's path is passed to GCM as additional authenticated data, so a
// payload cannot be moved to a different <testcase> without the tag failing —
// the report's structure is authenticated even though it is not encrypted.
const (
	envelopeHeader = "koc-enc:v1:rsa-oaep-sha256:aes-256-gcm"
	gcmNonceLen    = 12
	// minRecipientBits rejects keys too small to wrap a 256-bit key safely; 2048
	// is the floor every current guideline agrees on.
	minRecipientBits = 2048
)

// encryptPayload seals plaintext for the recipient, binding it to aad (the
// secret's path). The result is the header line plus base64, ready to embed.
func encryptPayload(pub *rsa.PublicKey, aad string, plaintext []byte) (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generating a data key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcmNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating a nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plaintext, []byte(aad))

	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, key, nil)
	if err != nil {
		return "", fmt.Errorf("wrapping the data key for the recipient: %w", err)
	}

	buf := make([]byte, 0, 2+len(wrapped)+len(nonce)+len(ct))
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(wrapped))) //nolint:gosec // G115: RSA-4096 wraps to 512 bytes, far below uint16
	buf = append(buf, wrapped...)
	buf = append(buf, nonce...)
	buf = append(buf, ct...)

	return envelopeHeader + "\n" + base64.StdEncoding.EncodeToString(buf), nil
}

// decryptPayload opens an envelope with the recipient's private key. aad must be
// the path the payload was sealed under.
func decryptPayload(priv *rsa.PrivateKey, aad, envelope string) ([]byte, error) {
	header, b64, ok := strings.Cut(strings.TrimSpace(envelope), "\n")
	if !ok {
		return nil, fmt.Errorf("payload is not a koc envelope (no header line)")
	}
	if strings.TrimSpace(header) != envelopeHeader {
		return nil, fmt.Errorf("unsupported payload format %q, want %q", strings.TrimSpace(header), envelopeHeader)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(b64), ""))
	if err != nil {
		return nil, fmt.Errorf("decoding payload: %w", err)
	}
	if len(raw) < 2 {
		return nil, fmt.Errorf("payload is truncated")
	}
	wrappedLen := int(binary.BigEndian.Uint16(raw[:2]))
	if len(raw) < 2+wrappedLen+gcmNonceLen {
		return nil, fmt.Errorf("payload is truncated")
	}
	wrapped := raw[2 : 2+wrappedLen]
	nonce := raw[2+wrappedLen : 2+wrappedLen+gcmNonceLen]
	ct := raw[2+wrappedLen+gcmNonceLen:]

	key, err := rsa.DecryptOAEP(sha256.New(), nil, priv, wrapped, nil)
	if err != nil {
		return nil, fmt.Errorf("unwrapping the data key (wrong identity?): %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, ct, []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("authenticating payload for %q (tampered or misplaced?): %w", aad, err)
	}
	return plaintext, nil
}

// loadRecipient reads an RSA public key from a PEM file. It accepts a bare
// public key (PKIX or PKCS#1) or a certificate, so an existing PKI-issued cert
// can be used as the recipient without extracting the key first.
func loadRecipient(path string) (*rsa.PublicKey, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied recipient key path
	if err != nil {
		return nil, fmt.Errorf("reading --recipient %q: %w", path, err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("--recipient %q is not PEM (expected a PUBLIC KEY or CERTIFICATE block)", path)
	}

	var pub any
	switch block.Type {
	case "PUBLIC KEY":
		pub, err = x509.ParsePKIXPublicKey(block.Bytes)
	case "RSA PUBLIC KEY":
		pub, err = x509.ParsePKCS1PublicKey(block.Bytes)
	case "CERTIFICATE":
		var cert *x509.Certificate
		if cert, err = x509.ParseCertificate(block.Bytes); err == nil {
			pub = cert.PublicKey
		}
	default:
		return nil, fmt.Errorf("--recipient %q holds a %q block; expected PUBLIC KEY or CERTIFICATE", path, block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("parsing --recipient %q: %w", path, err)
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("--recipient %q is a %T; only RSA keys are supported", path, pub)
	}
	if bits := rsaPub.N.BitLen(); bits < minRecipientBits {
		return nil, fmt.Errorf("--recipient %q is a %d-bit key; %d bits is the minimum", path, bits, minRecipientBits)
	}
	return rsaPub, nil
}

// loadIdentity reads an RSA private key (PKCS#8 or PKCS#1) from a PEM file.
func loadIdentity(path string) (*rsa.PrivateKey, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied identity key path
	if err != nil {
		return nil, fmt.Errorf("reading --identity %q: %w", path, err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("--identity %q is not PEM (expected a PRIVATE KEY block)", path)
	}
	if _, encrypted := block.Headers["DEK-Info"]; encrypted {
		return nil, fmt.Errorf("--identity %q is a legacy encrypted PEM; convert it first: openssl pkey -in %s -out plain.pem", path, path)
	}

	var key any
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "ENCRYPTED PRIVATE KEY":
		return nil, fmt.Errorf("--identity %q is passphrase-encrypted; decrypt it first: openssl pkey -in %s -out plain.pem", path, path)
	default:
		return nil, fmt.Errorf("--identity %q holds a %q block; expected PRIVATE KEY", path, block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("parsing --identity %q: %w", path, err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("--identity %q is a %T; only RSA keys are supported", path, key)
	}
	return rsaKey, nil
}
