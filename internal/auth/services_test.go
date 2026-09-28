package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
)

func TestAPIMicroversion(t *testing.T) {
	for _, tc := range []struct {
		major   int
		in      string
		want    string
		wantErr bool
	}{
		{3, "3", "latest", false},
		{2, "2", "latest", false},
		{1, "1", "latest", false},
		{3, "3.0", "3.0", false},
		{3, "3.70", "3.70", false},
		{2, "2.93", "2.93", false},
		{3, "latest", "latest", false},
		{3, "", "", false},
		{3, "2", "", true}, // cinder v2: removed before Zed
		{2, "1", "", true},
		{1, "2", "", true},
	} {
		got, err := apiMicroversion("os-x-api-version", tc.major, tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("apiMicroversion(%d, %q) = %q, %v; want %q, error=%v",
				tc.major, tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

// headerCapture is a mock endpoint recording the microversion header of the
// last request it served.
func headerCapture(t *testing.T, header string) (*httptest.Server, *string) {
	t.Helper()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(header)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func clientAt(srv *httptest.Server, opts *Options) *Client {
	return &Client{
		Provider: &gophercloud.ProviderClient{
			TokenID:         "token",
			HTTPClient:      *srv.Client(),
			EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return srv.URL + "/", nil },
		},
		opts: opts,
	}
}

// TestFactories_MajorOnlyVersionNegotiates drives the real factories: an
// openrc's OS_VOLUME_API_VERSION=3 used to reach cinder as "volume 3", which it
// rejects with 400, and the other microversioned services had the same flaw.
func TestFactories_MajorOnlyVersionNegotiates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opts   *Options
		derive func(*Client) (*gophercloud.ServiceClient, error)
		header string
		want   string
	}{
		{"volume", &Options{VolumeAPIVersion: "3"}, (*Client).Volume,
			"OpenStack-API-Version", "volume latest"},
		{"compute", &Options{ComputeAPIVersion: "2"}, (*Client).Compute,
			"OpenStack-API-Version", "compute latest"},
		{"placement", &Options{PlacementAPIVersion: "1"}, (*Client).Placement,
			"OpenStack-API-Version", "placement latest"},
		{"baremetal", &Options{BaremetalAPIVersion: "1"}, (*Client).Baremetal,
			"X-OpenStack-Ironic-API-Version", "latest"},
		// A real microversion still goes out exactly as given.
		{"volume pinned", &Options{VolumeAPIVersion: "3.44"}, (*Client).Volume,
			"OpenStack-API-Version", "volume 3.44"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := headerCapture(t, tc.header)
			sc, err := tc.derive(clientAt(srv, tc.opts))
			if err != nil {
				t.Fatalf("factory: %v", err)
			}
			resp, err := sc.Get(context.Background(), sc.ServiceURL("x"), nil, nil)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			_ = resp.Body.Close()
			if *got != tc.want {
				t.Errorf("%s = %q, want %q", tc.header, *got, tc.want)
			}
		})
	}
}

func TestVolume_ForeignMajorVersionNamesTheFlag(t *testing.T) {
	srv, _ := headerCapture(t, "OpenStack-API-Version")
	_, err := clientAt(srv, &Options{VolumeAPIVersion: "2"}).Volume()
	if err == nil || !strings.Contains(err.Error(), "--os-volume-api-version 2") {
		t.Fatalf("Volume() error = %v, want one naming --os-volume-api-version 2", err)
	}
}

// The current project is the token's, however the credentials named it.
func TestScopedProjectID(t *testing.T) {
	scoped := func(token map[string]any) *Client {
		t.Helper()
		var r tokens.CreateResult
		r.Body = map[string]any{"token": token}
		pc := &gophercloud.ProviderClient{}
		if err := pc.SetTokenAndAuthResult(r); err != nil {
			t.Fatal(err)
		}
		return &Client{Provider: pc}
	}
	if got := scoped(map[string]any{"project": map[string]any{"id": "p1", "name": "demo"}}).ScopedProjectID(); got != "p1" {
		t.Errorf("project-scoped token: ScopedProjectID() = %q, want p1", got)
	}
	if got := scoped(map[string]any{"domain": map[string]any{"id": "d1"}}).ScopedProjectID(); got != "" {
		t.Errorf("domain-scoped token: ScopedProjectID() = %q, want empty", got)
	}
	if got := (&Client{}).ScopedProjectID(); got != "" {
		t.Errorf("no keystone provider: ScopedProjectID() = %q, want empty", got)
	}
}
