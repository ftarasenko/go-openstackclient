package auth

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// resolveTLSConfig overlays the CLI/env TLS settings onto an optional base
// config (the one clouds.Parse derives from a clouds.yaml entry's cacert / cert
// / key / verify fields). Explicit flags/env win over clouds.yaml; anything not
// specified is left as the base provides it. A TLS 1.2 minimum is always
// enforced.
//
// It returns the resolved config plus whether TLS verification is being skipped,
// so the caller can emit the mandated --insecure warning.
func (o *Options) resolveTLSConfig(base *tls.Config) (*tls.Config, bool, error) {
	cfg := base
	if cfg == nil {
		cfg = &tls.Config{}
	} else {
		cfg = cfg.Clone()
	}

	if cfg.MinVersion == 0 {
		cfg.MinVersion = tls.VersionTLS12
	}

	// Custom CA bundle for our internal PKI.
	if o.CACert != "" {
		pem, err := os.ReadFile(o.CACert)
		if err != nil {
			return nil, false, fmt.Errorf("reading CA bundle %q: %w", o.CACert, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(bytes.TrimSpace(pem)) {
			return nil, false, fmt.Errorf("no certificates parsed from CA bundle %q", o.CACert)
		}
		cfg.RootCAs = pool
	}

	// Mutual TLS: client certificate + key must be provided together. In-memory
	// PEM (e.g. loaded from Vault by --creds-from-vault) wins over file paths.
	switch {
	case len(o.ClientCertPEM) > 0 && len(o.ClientKeyPEM) > 0:
		cert, err := tls.X509KeyPair(o.ClientCertPEM, o.ClientKeyPEM)
		if err != nil {
			return nil, false, fmt.Errorf("loading in-memory client certificate/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	case o.ClientCert != "" && o.ClientKey != "":
		cert, err := tls.LoadX509KeyPair(o.ClientCert, o.ClientKey)
		if err != nil {
			return nil, false, fmt.Errorf("loading client certificate/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	case o.ClientCert != "" && o.ClientKey == "":
		return nil, false, fmt.Errorf("--os-cert is set but --os-key is missing")
	case o.ClientCert == "" && o.ClientKey != "":
		return nil, false, fmt.Errorf("--os-key is set but --os-cert is missing")
	}

	// Only override verification when the user explicitly asked, so a
	// clouds.yaml "verify: false" is preserved when no flag/env is given.
	if o.insecureExplicit() {
		cfg.InsecureSkipVerify = o.Insecure
	}

	return cfg, cfg.InsecureSkipVerify, nil
}
