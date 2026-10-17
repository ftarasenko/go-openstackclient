package keyvrm

import (
	"context"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
)

// Update bodies are maps of the API's JSON field names: KeyVRM updates with
// exclude_none semantics, so only the keys present are changed.

// okCodes is the RequestOpts accepting codes: 200 for reads and updates, any
// 2xx success for the bodiless triggers.
func okCodes(codes ...int) *gophercloud.RequestOpts {
	return &gophercloud.RequestOpts{OkCodes: codes}
}

func okGet() *gophercloud.RequestOpts { return okCodes(http.StatusOK) }
func okPut() *gophercloud.RequestOpts { return okCodes(http.StatusOK) }
func okTrigger() *gophercloud.RequestOpts {
	return okCodes(http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent)
}

// closeResp closes the response body. gophercloud closes it itself when a
// JSONResponse is decoded, but not for bodiless trigger calls, so closing here
// covers both. A second Close is harmless.
func closeResp(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func get[T any](ctx context.Context, sc *gophercloud.ServiceClient, url string) (*T, error) {
	var out T
	resp, err := sc.Get(ctx, url, &out, okGet())
	closeResp(resp)
	return &out, err
}

func put[T any](ctx context.Context, sc *gophercloud.ServiceClient, url string, body map[string]any) (*T, error) {
	var out T
	resp, err := sc.Put(ctx, url, body, &out, okPut())
	closeResp(resp)
	return &out, err
}

func trigger(ctx context.Context, sc *gophercloud.ServiceClient, url string) error {
	resp, err := sc.Post(ctx, url, nil, nil, okTrigger())
	closeResp(resp)
	return err
}

// --- app config ---

// GetAppConfig reads the global application configuration (GET /v1/app_config).
func GetAppConfig(ctx context.Context, sc *gophercloud.ServiceClient) (*AppConfig, error) {
	return get[AppConfig](ctx, sc, sc.ServiceURL("app_config"))
}

// UpdateAppConfig changes the global application configuration
// (PUT /v1/app_config) and returns the result.
func UpdateAppConfig(ctx context.Context, sc *gophercloud.ServiceClient, body map[string]any) (*AppConfig, error) {
	return put[AppConfig](ctx, sc, sc.ServiceURL("app_config"), body)
}

// --- host aggregate config ---

// ListHostAggregates lists host-aggregate configurations.
func ListHostAggregates(ctx context.Context, sc *gophercloud.ServiceClient, opts ListOpts) (*Page[HostAggregateConfig], error) {
	return get[Page[HostAggregateConfig]](ctx, sc, sc.ServiceURL("host_aggregates")+opts.ToQuery())
}

// GetHostAggregate reads one host-aggregate configuration.
func GetHostAggregate(ctx context.Context, sc *gophercloud.ServiceClient, id string) (*HostAggregateConfig, error) {
	return get[HostAggregateConfig](ctx, sc, sc.ServiceURL("host_aggregates", id))
}

// UpdateHostAggregate changes a host-aggregate configuration and returns the
// result.
func UpdateHostAggregate(ctx context.Context, sc *gophercloud.ServiceClient, id string, body map[string]any) (*HostAggregateConfig, error) {
	return put[HostAggregateConfig](ctx, sc, sc.ServiceURL("host_aggregates", id), body)
}

// GetMarkers lists the markers a host aggregate may carry ("LB", "HA", …).
func GetMarkers(ctx context.Context, sc *gophercloud.ServiceClient) ([]string, error) {
	out, err := get[[]string](ctx, sc, sc.ServiceURL("host_aggregates", "markers"))
	return *out, err
}

// ListHostAggregateEvents lists the events of one host aggregate.
func ListHostAggregateEvents(ctx context.Context, sc *gophercloud.ServiceClient, haID string, opts ListOpts) (*Page[Event], error) {
	return get[Page[Event]](ctx, sc, sc.ServiceURL("host_aggregates", haID, "events")+opts.ToQuery())
}

// --- availability zone ---

// ListAvailabilityZones lists availability-zone summaries.
func ListAvailabilityZones(ctx context.Context, sc *gophercloud.ServiceClient, opts ListOpts) (*Page[AvailabilityZone], error) {
	return get[Page[AvailabilityZone]](ctx, sc, sc.ServiceURL("azones")+opts.ToQuery())
}

// ListZoneHostAggregates lists the host-aggregate configurations of one
// availability zone.
func ListZoneHostAggregates(ctx context.Context, sc *gophercloud.ServiceClient, az string, opts ListOpts) (*Page[HostAggregateConfig], error) {
	return get[Page[HostAggregateConfig]](ctx, sc, sc.ServiceURL("azones", az, "host_aggregates")+opts.ToQuery())
}

// --- event ---

// GetEvent reads one host-aggregate event.
func GetEvent(ctx context.Context, sc *gophercloud.ServiceClient, id string) (*Event, error) {
	return get[Event](ctx, sc, sc.ServiceURL("host_aggregate_events", id))
}

// ListEventRecommendations lists the recommendations an event produced.
func ListEventRecommendations(ctx context.Context, sc *gophercloud.ServiceClient, eventID string, opts ListOpts) (*Page[Recommendation], error) {
	return get[Page[Recommendation]](ctx, sc, sc.ServiceURL("host_aggregate_events", eventID, "recommendations")+opts.ToQuery())
}

// RunEventRecommendations starts every recommendation of an event.
func RunEventRecommendations(ctx context.Context, sc *gophercloud.ServiceClient, eventID string) error {
	return trigger(ctx, sc, sc.ServiceURL("host_aggregate_events", eventID, "recommendations", "run"))
}

// --- recommendation ---

// ListRecommendations lists recommendations.
func ListRecommendations(ctx context.Context, sc *gophercloud.ServiceClient, opts ListOpts) (*Page[Recommendation], error) {
	return get[Page[Recommendation]](ctx, sc, sc.ServiceURL("recommendations")+opts.ToQuery())
}

// GetRecommendation reads one recommendation.
func GetRecommendation(ctx context.Context, sc *gophercloud.ServiceClient, id string) (*Recommendation, error) {
	return get[Recommendation](ctx, sc, sc.ServiceURL("recommendations", id))
}

// ListRecommendationOperations lists the operations a recommendation executed.
func ListRecommendationOperations(ctx context.Context, sc *gophercloud.ServiceClient, recID string, opts ListOpts) (*Page[Operation], error) {
	return get[Page[Operation]](ctx, sc, sc.ServiceURL("recommendations", recID, "operations")+opts.ToQuery())
}

// RunRecommendation starts a recommendation.
func RunRecommendation(ctx context.Context, sc *gophercloud.ServiceClient, id string) error {
	return trigger(ctx, sc, sc.ServiceURL("recommendations", id, "run"))
}

// StopRecommendation stops a running recommendation.
func StopRecommendation(ctx context.Context, sc *gophercloud.ServiceClient, id string) error {
	return trigger(ctx, sc, sc.ServiceURL("recommendations", id, "stop"))
}
