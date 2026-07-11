package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
)

// writeSelfSigned generates a self-signed cert+key pair and writes them as PEM
// files, returning their paths.
func writeSelfSigned(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "koc-test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestResolveTLSConfig_Defaults(t *testing.T) {
	o := &Options{}
	o.AddFlags(pflag.NewFlagSet("t", pflag.ContinueOnError))

	cfg, insecure, err := o.resolveTLSConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if insecure {
		t.Error("verification should be enabled by default")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2", cfg.MinVersion)
	}
}

func TestResolveTLSConfig_CABundle(t *testing.T) {
	dir := t.TempDir()
	caPath, _ := writeSelfSigned(t, dir)

	o := &Options{}
	o.AddFlags(pflag.NewFlagSet("t", pflag.ContinueOnError))
	o.CACert = caPath

	cfg, _, err := o.resolveTLSConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RootCAs == nil {
		t.Error("RootCAs should be populated from the CA bundle")
	}
}

func TestResolveTLSConfig_MutualTLS(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir)

	o := &Options{}
	o.AddFlags(pflag.NewFlagSet("t", pflag.ContinueOnError))
	o.ClientCert, o.ClientKey = certPath, keyPath

	cfg, _, err := o.resolveTLSConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Certificates) != 1 {
		t.Errorf("expected 1 client certificate, got %d", len(cfg.Certificates))
	}
}

func TestResolveTLSConfig_ClientCertRequiresKey(t *testing.T) {
	dir := t.TempDir()
	certPath, _ := writeSelfSigned(t, dir)

	o := &Options{}
	o.AddFlags(pflag.NewFlagSet("t", pflag.ContinueOnError))
	o.ClientCert = certPath

	if _, _, err := o.resolveTLSConfig(nil); err == nil {
		t.Error("expected error when client cert is set without a key")
	}
}

func TestResolveTLSConfig_InsecureExplicit(t *testing.T) {
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	o := &Options{}
	o.AddFlags(fs)
	if err := fs.Set("insecure", "true"); err != nil {
		t.Fatal(err)
	}

	cfg, insecure, err := o.resolveTLSConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !insecure || !cfg.InsecureSkipVerify {
		t.Error("explicit --insecure should disable verification")
	}
}

func TestResolveTLSConfig_PreservesCloudsVerifyWhenFlagUnset(t *testing.T) {
	// A clouds.yaml "verify: false" arrives as a base config with
	// InsecureSkipVerify=true; without an explicit flag we must not clobber it.
	o := &Options{}
	o.AddFlags(pflag.NewFlagSet("t", pflag.ContinueOnError))

	base := &tls.Config{InsecureSkipVerify: true}
	_, insecure, err := o.resolveTLSConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	if !insecure {
		t.Error("clouds.yaml verify:false should be preserved when no flag is given")
	}
}
