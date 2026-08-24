package kube

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests pin what clusterTLS and userAuth do for every source a kubeconfig
// can name a credential from — inline base64, a file path, or neither — because
// each source is a different way for an operator's cluster access to silently
// stop working.

// selfSigned returns a self-signed certificate and its key, both PEM-encoded.
func selfSigned(t *testing.T, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// writeTemp writes b to a file in the test's temp dir and returns its path.
func writeTemp(t *testing.T, name string, b []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

type clusterFields = struct {
	Server   string `yaml:"server"`
	CAData   string `yaml:"certificate-authority-data"`
	CAFile   string `yaml:"certificate-authority"`
	Insecure bool   `yaml:"insecure-skip-tls-verify"`
}

// cluster builds a one-cluster kubeconfig, filled in by mutate.
func cluster(name string, mutate func(c *clusterFields)) *kubeconfig {
	var kc kubeconfig
	kc.Clusters = append(kc.Clusters, struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server   string `yaml:"server"`
			CAData   string `yaml:"certificate-authority-data"`
			CAFile   string `yaml:"certificate-authority"`
			Insecure bool   `yaml:"insecure-skip-tls-verify"`
		} `yaml:"cluster"`
	}{Name: name})
	mutate(&kc.Clusters[0].Cluster)
	return &kc
}

func TestClusterTLS_CASources(t *testing.T) {
	caPEM, _ := selfSigned(t, "test-ca")
	caFile := writeTemp(t, "ca.pem", caPEM)

	tests := []struct {
		name        string
		caData      string
		caFile      string
		insecure    bool
		server      string
		wantRoots   bool
		wantSkip    bool
		wantWarning bool
		wantErr     string
	}{
		{
			name: "inline base64 CA becomes the root pool",
			// certificate-authority-data is what a k0s admin.conf carries.
			caData: b64(caPEM), server: "https://apiserver.example.com:6443",
			wantRoots: true,
		},
		{
			name:   "a CA file path becomes the root pool",
			caFile: caFile, server: "https://apiserver.example.com:6443",
			wantRoots: true,
		},
		{
			// Inline data wins; the file is not even opened.
			name:   "inline data takes precedence over the file",
			caData: b64(caPEM), caFile: "/nonexistent/ca.pem",
			server: "https://apiserver.example.com:6443", wantRoots: true,
		},
		{
			// No CA at all means the system roots, which is a valid setup for a
			// publicly-trusted apiserver — not an error.
			name:   "no CA at all leaves the system roots in place",
			server: "https://apiserver.example.com:6443",
		},
		{
			name:     "insecure-skip-tls-verify skips the CA entirely and warns",
			insecure: true, caData: "not-valid-base64!!", // never looked at
			server:      "https://apiserver.example.com:6443",
			wantSkip:    true,
			wantWarning: true,
		},
		{
			name:   "an undecodable inline CA is an error",
			caData: "not-valid-base64!!", server: "https://apiserver.example.com:6443",
			wantErr: `cluster "c" CA: decoding inline base64`,
		},
		{
			name:   "a missing CA file is an error",
			caFile: "/nonexistent/ca.pem", server: "https://apiserver.example.com:6443",
			wantErr: `cluster "c" CA:`,
		},
		{
			name:   "a CA holding no certificates is an error",
			caData: b64([]byte("not a pem block")), server: "https://apiserver.example.com:6443",
			wantErr: `cluster "c": no certificates parsed from CA`,
		},
		{
			name:    "a cluster with no server URL is an error",
			caData:  b64(caPEM),
			wantErr: `cluster "c" has no server URL`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kc := cluster("c", func(c *clusterFields) {
				c.Server, c.CAData, c.CAFile, c.Insecure = tt.server, tt.caData, tt.caFile, tt.insecure
			})

			var server string
			var cfg *tls.Config
			var err error
			out := captureStderr(t, func() {
				server, cfg, err = clusterTLS(kc, "c")
			})

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("clusterTLS() error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("clusterTLS() error = %v", err)
			}
			if server != tt.server {
				t.Errorf("server = %q, want %q", server, tt.server)
			}
			if cfg.MinVersion != tls.VersionTLS12 {
				t.Errorf("MinVersion = %x, want TLS 1.2", cfg.MinVersion)
			}
			if (cfg.RootCAs != nil) != tt.wantRoots {
				t.Errorf("RootCAs set = %v, want %v", cfg.RootCAs != nil, tt.wantRoots)
			}
			if cfg.InsecureSkipVerify != tt.wantSkip {
				t.Errorf("InsecureSkipVerify = %v, want %v", cfg.InsecureSkipVerify, tt.wantSkip)
			}
			gotWarning := strings.Contains(out, "TLS certificate verification is disabled")
			if gotWarning != tt.wantWarning {
				t.Errorf("insecure warning = %v, want %v (stderr: %s)", gotWarning, tt.wantWarning, out)
			}
		})
	}
}

func TestClusterTLS_UnknownCluster(t *testing.T) {
	kc := cluster("c", func(c *clusterFields) {
		c.Server = "https://apiserver.example.com:6443"
	})
	if _, _, err := clusterTLS(kc, "other"); err == nil ||
		!strings.Contains(err.Error(), `cluster "other" not found in kubeconfig`) {
		t.Fatalf("clusterTLS() error = %v, want the not-found message", err)
	}
}

type userFields = struct {
	ClientCertData string `yaml:"client-certificate-data"`
	ClientCertFile string `yaml:"client-certificate"`
	ClientKeyData  string `yaml:"client-key-data"`
	ClientKeyFile  string `yaml:"client-key"`
	Token          string `yaml:"token"`
	TokenFile      string `yaml:"tokenFile"`
}

// user builds a one-user kubeconfig, filled in by mutate.
func user(name string, mutate func(u *userFields)) *kubeconfig {
	var kc kubeconfig
	kc.Users = append(kc.Users, struct {
		Name string `yaml:"name"`
		User struct {
			ClientCertData string `yaml:"client-certificate-data"`
			ClientCertFile string `yaml:"client-certificate"`
			ClientKeyData  string `yaml:"client-key-data"`
			ClientKeyFile  string `yaml:"client-key"`
			Token          string `yaml:"token"`
			TokenFile      string `yaml:"tokenFile"`
		} `yaml:"user"`
	}{Name: name})
	mutate(&kc.Users[0].User)
	return &kc
}

func TestUserAuth_CredentialSources(t *testing.T) {
	certPEM, keyPEM := selfSigned(t, "admin")
	dir := t.TempDir()
	certFile := filepath.Join(dir, "client.crt")
	keyFile := filepath.Join(dir, "client.key")
	tokenFile := filepath.Join(dir, "token")
	for path, b := range map[string][]byte{
		certFile:  certPEM,
		keyFile:   keyPEM,
		tokenFile: []byte("file-token\n"),
	} {
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name     string
		set      func(u *userFields)
		wantCert bool
		want     string
		wantErr  string
	}{
		{
			// Mutual TLS is how k0s admin.conf authenticates: no bearer token at
			// all, so an empty token here is success, not a missing credential.
			name: "inline client certificate and key become mutual TLS",
			set: func(u *userFields) {
				u.ClientCertData, u.ClientKeyData = b64(certPEM), b64(keyPEM)
			},
			wantCert: true,
		},
		{
			name: "client certificate and key file paths become mutual TLS",
			set: func(u *userFields) {
				u.ClientCertFile, u.ClientKeyFile = certFile, keyFile
			},
			wantCert: true,
		},
		{
			name: "an inline certificate pairs with a key file",
			set: func(u *userFields) {
				u.ClientCertData, u.ClientKeyFile = b64(certPEM), keyFile
			},
			wantCert: true,
		},
		{
			name: "a bearer token is returned for the Authorization header",
			set:  func(u *userFields) { u.Token = "sekret-token" },
			want: "sekret-token",
		},
		{
			// The file's bytes are returned verbatim, trailing newline included.
			name: "a token file is read verbatim",
			set:  func(u *userFields) { u.TokenFile = tokenFile },
			want: "file-token\n",
		},
		{
			// A client certificate outranks a token even when both are present.
			name: "a client certificate wins over a token",
			set: func(u *userFields) {
				u.ClientCertData, u.ClientKeyData, u.Token = b64(certPEM), b64(keyPEM), "ignored"
			},
			wantCert: true,
		},
		{
			name: "an inline token wins over a token file",
			set:  func(u *userFields) { u.Token, u.TokenFile = "inline", tokenFile },
			want: "inline",
		},
		{
			name:    "a user with nothing usable is an error",
			set:     func(*userFields) {},
			wantErr: `user "u" has no usable credentials (client cert or token)`,
		},
		{
			name:    "a certificate with no key is an error",
			set:     func(u *userFields) { u.ClientCertData = b64(certPEM) },
			wantErr: `user "u" client certificate/key`,
		},
		{
			name:    "an undecodable inline certificate is an error",
			set:     func(u *userFields) { u.ClientCertData = "not-base64!!" },
			wantErr: `user "u" client certificate: decoding inline base64`,
		},
		{
			name: "an undecodable inline key is an error",
			set: func(u *userFields) {
				u.ClientCertData, u.ClientKeyData = b64(certPEM), "not-base64!!"
			},
			wantErr: `user "u" client key: decoding inline base64`,
		},
		{
			name:    "a missing certificate file is an error",
			set:     func(u *userFields) { u.ClientCertFile = filepath.Join(dir, "absent.crt") },
			wantErr: `user "u" client certificate:`,
		},
		{
			name: "a missing key file is an error",
			set: func(u *userFields) {
				u.ClientCertFile, u.ClientKeyFile = certFile, filepath.Join(dir, "absent.key")
			},
			wantErr: `user "u" client key:`,
		},
		{
			name:    "a missing token file is an error",
			set:     func(u *userFields) { u.TokenFile = filepath.Join(dir, "absent.token") },
			wantErr: `user "u" token file:`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kc := user("u", tt.set)
			cfg := &tls.Config{MinVersion: tls.VersionTLS12}
			got, err := userAuth(kc, "u", cfg)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("userAuth() error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("userAuth() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("userAuth() token = %q, want %q", got, tt.want)
			}
			if hasCert := len(cfg.Certificates) == 1; hasCert != tt.wantCert {
				t.Errorf("client certificate installed = %v, want %v", hasCert, tt.wantCert)
			}
		})
	}
}

func TestUserAuth_UnknownUser(t *testing.T) {
	kc := user("u", func(u *userFields) { u.Token = "t" })
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if _, err := userAuth(kc, "other", cfg); err == nil ||
		!strings.Contains(err.Error(), `user "other" not found in kubeconfig`) {
		t.Fatalf("userAuth() error = %v, want the not-found message", err)
	}
}
