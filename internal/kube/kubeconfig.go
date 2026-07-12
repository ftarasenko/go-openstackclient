// Package kube is a dependency-free, read-only Kubernetes REST client. It exists
// so koc can pull credentials straight out of a cluster (an Ironic instance's
// basic-auth secret) without vendoring client-go, honoring the repo's air-gap /
// minimal-dependency invariant. It parses a kubeconfig with the already-vendored
// yaml.v3 and talks to the apiserver over stdlib net/http.
package kube

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// kubeconfig is the minimal subset of a kubeconfig document koc needs to reach
// the apiserver: a server URL + CA to trust, and either a client certificate or
// a bearer token to authenticate.
type kubeconfig struct {
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server   string `yaml:"server"`
			CAData   string `yaml:"certificate-authority-data"`
			CAFile   string `yaml:"certificate-authority"`
			Insecure bool   `yaml:"insecure-skip-tls-verify"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			ClientCertData string `yaml:"client-certificate-data"`
			ClientCertFile string `yaml:"client-certificate"`
			ClientKeyData  string `yaml:"client-key-data"`
			ClientKeyFile  string `yaml:"client-key"`
			Token          string `yaml:"token"`
			TokenFile      string `yaml:"tokenFile"`
		} `yaml:"user"`
	} `yaml:"users"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster   string `yaml:"cluster"`
			User      string `yaml:"user"`
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	} `yaml:"contexts"`
}

// Options selects and configures a kubeconfig. Zero values fall back to the
// standard discovery (--kubeconfig → $KUBECONFIG → ~/.kube/config; current
// context; verification on).
type Options struct {
	Kubeconfig string
	Context    string
	Debug      bool
}

// Load resolves the kubeconfig, selects the context, and builds a ready REST
// Client for the target cluster.
func Load(o Options) (*Client, error) {
	path := resolveKubeconfigPath(o.Kubeconfig)
	raw, err := os.ReadFile(path) //nolint:gosec // G304: operator-controlled kubeconfig path (--kubeconfig/$KUBECONFIG)
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig %q: %w", path, err)
	}
	var kc kubeconfig
	if err := yaml.Unmarshal(raw, &kc); err != nil {
		return nil, fmt.Errorf("parsing kubeconfig %q: %w", path, err)
	}

	ctxName := o.Context
	if ctxName == "" {
		ctxName = kc.CurrentContext
	}
	if ctxName == "" {
		return nil, fmt.Errorf("kubeconfig %q has no current-context and no --kube-context was given", path)
	}

	var clusterName, userName string
	found := false
	for _, c := range kc.Contexts {
		if c.Name == ctxName {
			clusterName, userName = c.Context.Cluster, c.Context.User
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("context %q not found in kubeconfig %q", ctxName, path)
	}

	server, tlsCfg, err := clusterTLS(&kc, clusterName)
	if err != nil {
		return nil, err
	}
	token, err := userAuth(&kc, userName, tlsCfg)
	if err != nil {
		return nil, err
	}

	return &Client{
		server: server,
		token:  token,
		debug:  o.Debug,
		hc: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

// resolveKubeconfigPath applies the standard precedence: explicit path, then the
// first entry of $KUBECONFIG, then ~/.kube/config.
func resolveKubeconfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("KUBECONFIG"); env != "" {
		if parts := filepath.SplitList(env); len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".kube", "config")
	}
	return filepath.Join(home, ".kube", "config")
}

// clusterTLS resolves the named cluster's server URL and a *tls.Config trusting
// its CA (or skipping verification when the kubeconfig opts out).
func clusterTLS(kc *kubeconfig, name string) (string, *tls.Config, error) {
	for _, c := range kc.Clusters {
		if c.Name != name {
			continue
		}
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if c.Cluster.Insecure {
			cfg.InsecureSkipVerify = true
		} else {
			ca, err := pemFromDataOrFile(c.Cluster.CAData, c.Cluster.CAFile)
			if err != nil {
				return "", nil, fmt.Errorf("cluster %q CA: %w", name, err)
			}
			if len(ca) > 0 {
				pool := x509.NewCertPool()
				if !pool.AppendCertsFromPEM(ca) {
					return "", nil, fmt.Errorf("cluster %q: no certificates parsed from CA", name)
				}
				cfg.RootCAs = pool
			}
		}
		if c.Cluster.Server == "" {
			return "", nil, fmt.Errorf("cluster %q has no server URL", name)
		}
		return c.Cluster.Server, cfg, nil
	}
	return "", nil, fmt.Errorf("cluster %q not found in kubeconfig", name)
}

// userAuth wires the named user's credentials: a client certificate is added to
// tlsCfg (mutual TLS, as k0s admin.conf uses), otherwise a bearer token is
// returned for the Authorization header.
func userAuth(kc *kubeconfig, name string, tlsCfg *tls.Config) (string, error) {
	for _, u := range kc.Users {
		if u.Name != name {
			continue
		}
		switch {
		case u.User.ClientCertData != "" || u.User.ClientCertFile != "":
			certPEM, err := pemFromDataOrFile(u.User.ClientCertData, u.User.ClientCertFile)
			if err != nil {
				return "", fmt.Errorf("user %q client certificate: %w", name, err)
			}
			keyPEM, err := pemFromDataOrFile(u.User.ClientKeyData, u.User.ClientKeyFile)
			if err != nil {
				return "", fmt.Errorf("user %q client key: %w", name, err)
			}
			cert, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				return "", fmt.Errorf("user %q client certificate/key: %w", name, err)
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
			return "", nil
		case u.User.Token != "":
			return u.User.Token, nil
		case u.User.TokenFile != "":
			b, err := os.ReadFile(u.User.TokenFile)
			if err != nil {
				return "", fmt.Errorf("user %q token file: %w", name, err)
			}
			return string(b), nil
		}
		return "", fmt.Errorf("user %q has no usable credentials (client cert or token)", name)
	}
	return "", fmt.Errorf("user %q not found in kubeconfig", name)
}

// pemFromDataOrFile returns PEM bytes from an inline base64 "-data" field or,
// failing that, from a referenced file path. Empty when neither is set.
func pemFromDataOrFile(data, file string) ([]byte, error) {
	if data != "" {
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, fmt.Errorf("decoding inline base64: %w", err)
		}
		return b, nil
	}
	if file != "" {
		return os.ReadFile(file) //nolint:gosec // G304: cert/CA path from the operator's own kubeconfig
	}
	return nil, nil
}
