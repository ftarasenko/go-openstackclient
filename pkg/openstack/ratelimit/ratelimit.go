// Package ratelimit reads the rate limits nova and cinder report at /limits.
// Both answer in the same shape, so List takes either client. nova has
// reported none since its rate limiting was removed; cinder's come from its
// configuration.
package ratelimit

import (
	"context"
	"fmt"
	"net/url"

	"github.com/gophercloud/gophercloud/v2"
)

// Limit is one rate limit: a verb on the URIs Regex matches, Value calls per
// Unit, of which Remaining are left until NextAvailable.
type Limit struct {
	URI           string
	Regex         string
	Verb          string
	Value         int
	Remaining     int
	Unit          string
	NextAvailable string
}

// List lists the rate limits the client's service reports for the token's
// project, or for projectID when it is not empty (an admin call nova
// accepts).
func List(ctx context.Context, c *gophercloud.ServiceClient, projectID string) ([]Limit, error) {
	var body struct {
		Limits struct {
			Rate []struct {
				URI   string `json:"uri"`
				Regex string `json:"regex"`
				Limit []struct {
					Verb          string `json:"verb"`
					Value         int    `json:"value"`
					Remaining     int    `json:"remaining"`
					Unit          string `json:"unit"`
					NextAvailable string `json:"next-available"`
				} `json:"limit"`
			} `json:"rate"`
		} `json:"limits"`
	}
	u := c.ServiceURL("limits")
	if projectID != "" {
		u += "?tenant_id=" + url.QueryEscape(projectID)
	}
	resp, err := c.Get(ctx, u, &body, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("getting rate limits: %w", err)
	}
	var out []Limit
	for _, r := range body.Limits.Rate {
		for _, l := range r.Limit {
			out = append(out, Limit{URI: r.URI, Regex: r.Regex, Verb: l.Verb, Value: l.Value,
				Remaining: l.Remaining, Unit: l.Unit, NextAvailable: l.NextAvailable})
		}
	}
	return out, nil
}
