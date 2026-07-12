package keyvrm

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
)

// okGet/okPut/okTrigger are the accepted status codes for KeyVRM calls.
var (
	okGet     = &gophercloud.RequestOpts{OkCodes: []int{200}}
	okPut     = &gophercloud.RequestOpts{OkCodes: []int{200}}
	okTrigger = &gophercloud.RequestOpts{OkCodes: []int{200, 201, 202, 204}}
)

// closeResp closes the response body. gophercloud closes it itself when a
// JSONResponse is decoded, but not for bodiless trigger calls, so closing here
// covers both and keeps the linter happy. A second Close is harmless.
func closeResp(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

// query renders pagination + filters as a URL query string appended to base.
func query(base string, o listOpts) string {
	v := url.Values{}
	if o.Limit > 0 {
		v.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		v.Set("offset", strconv.Itoa(o.Offset))
	}
	for k, val := range o.filters {
		if val != "" {
			v.Set(k, val)
		}
	}
	if len(v) == 0 {
		return base
	}
	return base + "?" + v.Encode()
}

// --- app config ---

func getAppConfig(ctx context.Context, sc *gophercloud.ServiceClient) (*AppConfig, error) {
	var out AppConfig
	resp, err := sc.Get(ctx, sc.ServiceURL("app_config"), &out, okGet)
	closeResp(resp)
	return &out, err
}

func updateAppConfig(ctx context.Context, sc *gophercloud.ServiceClient, body map[string]any) (*AppConfig, error) {
	var out AppConfig
	resp, err := sc.Put(ctx, sc.ServiceURL("app_config"), body, &out, okPut)
	closeResp(resp)
	return &out, err
}

// --- host aggregate config ---

func listHostAggregates(ctx context.Context, sc *gophercloud.ServiceClient, o listOpts) (*page[HostAggregateConfig], error) {
	var out page[HostAggregateConfig]
	resp, err := sc.Get(ctx, query(sc.ServiceURL("host_aggregates"), o), &out, okGet)
	closeResp(resp)
	return &out, err
}

func getHostAggregate(ctx context.Context, sc *gophercloud.ServiceClient, id string) (*HostAggregateConfig, error) {
	var out HostAggregateConfig
	resp, err := sc.Get(ctx, sc.ServiceURL("host_aggregates", id), &out, okGet)
	closeResp(resp)
	return &out, err
}

func updateHostAggregate(ctx context.Context, sc *gophercloud.ServiceClient, id string, body map[string]any) (*HostAggregateConfig, error) {
	var out HostAggregateConfig
	resp, err := sc.Put(ctx, sc.ServiceURL("host_aggregates", id), body, &out, okPut)
	closeResp(resp)
	return &out, err
}

func getMarkers(ctx context.Context, sc *gophercloud.ServiceClient) ([]string, error) {
	var out []string
	resp, err := sc.Get(ctx, sc.ServiceURL("host_aggregates", "markers"), &out, okGet)
	closeResp(resp)
	return out, err
}

func listHostAggregateEvents(ctx context.Context, sc *gophercloud.ServiceClient, haID string, o listOpts) (*page[Event], error) {
	var out page[Event]
	resp, err := sc.Get(ctx, query(sc.ServiceURL("host_aggregates", haID, "events"), o), &out, okGet)
	closeResp(resp)
	return &out, err
}

// --- availability zone ---

func listAvailabilityZones(ctx context.Context, sc *gophercloud.ServiceClient, o listOpts) (*page[AvailabilityZone], error) {
	var out page[AvailabilityZone]
	resp, err := sc.Get(ctx, query(sc.ServiceURL("azones"), o), &out, okGet)
	closeResp(resp)
	return &out, err
}

func listZoneHostAggregates(ctx context.Context, sc *gophercloud.ServiceClient, az string, o listOpts) (*page[HostAggregateConfig], error) {
	var out page[HostAggregateConfig]
	resp, err := sc.Get(ctx, query(sc.ServiceURL("azones", az, "host_aggregates"), o), &out, okGet)
	closeResp(resp)
	return &out, err
}

// --- event ---

func getEvent(ctx context.Context, sc *gophercloud.ServiceClient, id string) (*Event, error) {
	var out Event
	resp, err := sc.Get(ctx, sc.ServiceURL("host_aggregate_events", id), &out, okGet)
	closeResp(resp)
	return &out, err
}

func listEventRecommendations(ctx context.Context, sc *gophercloud.ServiceClient, eventID string, o listOpts) (*page[Recommendation], error) {
	var out page[Recommendation]
	resp, err := sc.Get(ctx, query(sc.ServiceURL("host_aggregate_events", eventID, "recommendations"), o), &out, okGet)
	closeResp(resp)
	return &out, err
}

func runEventRecommendations(ctx context.Context, sc *gophercloud.ServiceClient, eventID string) error {
	resp, err := sc.Post(ctx, sc.ServiceURL("host_aggregate_events", eventID, "recommendations", "run"), nil, nil, okTrigger)
	closeResp(resp)
	return err
}

// --- recommendation ---

func listRecommendations(ctx context.Context, sc *gophercloud.ServiceClient, o listOpts) (*page[Recommendation], error) {
	var out page[Recommendation]
	resp, err := sc.Get(ctx, query(sc.ServiceURL("recommendations"), o), &out, okGet)
	closeResp(resp)
	return &out, err
}

func getRecommendation(ctx context.Context, sc *gophercloud.ServiceClient, id string) (*Recommendation, error) {
	var out Recommendation
	resp, err := sc.Get(ctx, sc.ServiceURL("recommendations", id), &out, okGet)
	closeResp(resp)
	return &out, err
}

func listRecommendationOperations(ctx context.Context, sc *gophercloud.ServiceClient, recID string, o listOpts) (*page[Operation], error) {
	var out page[Operation]
	resp, err := sc.Get(ctx, query(sc.ServiceURL("recommendations", recID, "operations"), o), &out, okGet)
	closeResp(resp)
	return &out, err
}

func runRecommendation(ctx context.Context, sc *gophercloud.ServiceClient, id string) error {
	resp, err := sc.Post(ctx, sc.ServiceURL("recommendations", id, "run"), nil, nil, okTrigger)
	closeResp(resp)
	return err
}

func stopRecommendation(ctx context.Context, sc *gophercloud.ServiceClient, id string) error {
	resp, err := sc.Post(ctx, sc.ServiceURL("recommendations", id, "stop"), nil, nil, okTrigger)
	closeResp(resp)
	return err
}
