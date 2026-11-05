package volume

import (
	"context"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
)

// Raw calls for the cinder APIs gophercloud has no typed package for, or
// whose typed package lacks the route.

// closeBody closes a raw call's response body, if there is one.
func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

// getJSON GETs url into out, accepting only 200.
func getJSON(ctx context.Context, c *gophercloud.ServiceClient, url string, out any) error {
	resp, err := c.Get(ctx, url, out, &gophercloud.RequestOpts{OkCodes: []int{200}})
	closeBody(resp)
	return err
}

// putJSON PUTs body to url, decoding the answer into out, accepting only 200.
func putJSON(ctx context.Context, c *gophercloud.ServiceClient, url string, body, out any) error {
	resp, err := c.Put(ctx, url, body, out, &gophercloud.RequestOpts{OkCodes: []int{200}})
	closeBody(resp)
	return err
}
