// Package extension reads the API extensions a service advertises at
// /extensions. nova, cinder and neutron answer in the same shape, so these
// calls take any of their clients.
//
// Errors are wrapped with what the call was doing; gophercloud's error stays
// reachable through errors.As, so gophercloud.ResponseCodeIs keeps working
// on it.
package extension

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
)

// Extension is one advertised API extension. A service may leave
// Namespace, Updated and Links empty.
type Extension struct {
	Name        string `json:"name"`
	Alias       string `json:"alias"`
	Description string `json:"description"`
	Namespace   string `json:"namespace"`
	Updated     string `json:"updated"`
	Links       []any  `json:"links"`
}

// List lists the extensions the client's service advertises.
func List(ctx context.Context, c *gophercloud.ServiceClient) ([]Extension, error) {
	var body struct {
		Extensions []Extension `json:"extensions"`
	}
	if err := getJSON(ctx, c, c.ServiceURL("extensions"), &body); err != nil {
		return nil, fmt.Errorf("listing extensions: %w", err)
	}
	return body.Extensions, nil
}

// Get returns the extension the client's service advertises under alias.
func Get(ctx context.Context, c *gophercloud.ServiceClient, alias string) (*Extension, error) {
	var body struct {
		Extension Extension `json:"extension"`
	}
	if err := getJSON(ctx, c, c.ServiceURL("extensions", alias), &body); err != nil {
		return nil, fmt.Errorf("showing extension %q: %w", alias, err)
	}
	return &body.Extension, nil
}

func getJSON(ctx context.Context, c *gophercloud.ServiceClient, url string, out any) error {
	resp, err := c.Get(ctx, url, out, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return err
}
