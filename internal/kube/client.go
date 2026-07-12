package kube

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
)

// Client is a minimal read-only Kubernetes REST client bound to one cluster.
type Client struct {
	server string
	token  string
	hc     *http.Client
	debug  bool
}

// getJSON performs an authenticated GET and decodes the JSON body into out.
// It never dumps response bodies (they carry secret material); with debug on it
// logs only method, path and status.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.server+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if c.debug {
		fmt.Fprintf(os.Stderr, "kube: GET %s -> %d\n", path, resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kube GET %s: %s: %s", path, resp.Status, apiError(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding %s response: %w", path, err)
	}
	return nil
}

// GetSecret fetches a Secret and returns its data with values base64-decoded.
func (c *Client) GetSecret(ctx context.Context, namespace, name string) (map[string][]byte, error) {
	var s struct {
		Data map[string]string `json:"data"`
	}
	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s", url.PathEscape(namespace), url.PathEscape(name))
	if err := c.getJSON(ctx, path, &s); err != nil {
		return nil, err
	}
	// The apiserver returns Secret data as base64 strings; decode to raw bytes.
	out := make(map[string][]byte, len(s.Data))
	for k, v := range s.Data {
		dec, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("secret %s/%s key %q: %w", namespace, name, k, err)
		}
		out[k] = dec
	}
	return out, nil
}

// GetConfigMap fetches a ConfigMap and returns its data map (plain strings).
func (c *Client) GetConfigMap(ctx context.Context, namespace, name string) (map[string]string, error) {
	var cm struct {
		Data map[string]string `json:"data"`
	}
	path := fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", url.PathEscape(namespace), url.PathEscape(name))
	if err := c.getJSON(ctx, path, &cm); err != nil {
		return nil, err
	}
	return cm.Data, nil
}

// IronicAPI captures the fields koc needs from an Ironic (ironic.metal3.io) CR to
// build a standalone client: which secret holds the API credentials, the VIP the
// API is published on, and the TLS certificate secret.
type IronicAPI struct {
	APICredentialsName string
	IPAddress          string
	APIPort            int
	TLSCertificateName string
}

// GetIronic resolves the single Ironic instance in a namespace and returns the
// API-relevant spec fields. It errors when zero or more than one Ironic exist,
// so credentials are never read from the wrong (or a rotated) instance.
func (c *Client) GetIronic(ctx context.Context, namespace string) (*IronicAPI, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				APICredentialsName string `json:"apiCredentialsName"`
				Networking         struct {
					IPAddress string `json:"ipAddress"`
					APIPort   int    `json:"apiPort"`
				} `json:"networking"`
				TLS struct {
					CertificateName string `json:"certificateName"`
				} `json:"tls"`
			} `json:"spec"`
		} `json:"items"`
	}
	path := fmt.Sprintf("/apis/ironic.metal3.io/v1alpha1/namespaces/%s/ironics", url.PathEscape(namespace))
	if err := c.getJSON(ctx, path, &list); err != nil {
		return nil, err
	}
	switch len(list.Items) {
	case 0:
		return nil, fmt.Errorf("no Ironic instance found in namespace %q", namespace)
	case 1:
		it := list.Items[0]
		if it.Spec.APICredentialsName == "" {
			return nil, fmt.Errorf("ironic %s/%s has no spec.apiCredentialsName", namespace, it.Metadata.Name)
		}
		return &IronicAPI{
			APICredentialsName: it.Spec.APICredentialsName,
			IPAddress:          it.Spec.Networking.IPAddress,
			APIPort:            it.Spec.Networking.APIPort,
			TLSCertificateName: it.Spec.TLS.CertificateName,
		}, nil
	default:
		names := make([]string, 0, len(list.Items))
		for _, it := range list.Items {
			names = append(names, it.Metadata.Name)
		}
		return nil, fmt.Errorf("namespace %q holds %d Ironic instances (%v); expected exactly one", namespace, len(list.Items), names)
	}
}

// apiError extracts a concise message from a Kubernetes Status error body,
// falling back to the raw (truncated) payload.
func apiError(body []byte) string {
	var st struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &st) == nil && st.Message != "" {
		return st.Message
	}
	if len(body) > 300 {
		body = body[:300]
	}
	return string(body)
}
