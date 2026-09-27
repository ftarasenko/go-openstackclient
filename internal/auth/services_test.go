package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
)

func TestVolumeMicroversion(t *testing.T) {
	cases := map[string]string{
		"3":      "3.0",
		"3.0":    "3.0",
		"3.70":   "3.70",
		"latest": "latest",
		"":       "",
		"0":      "0", // not a major version cinder has; left for cinder to reject
		"-3":     "-3",
	}
	for in, want := range cases {
		if got := volumeMicroversion(in); got != want {
			t.Errorf("volumeMicroversion(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestVolume_MajorOnlyVersionSendsBaseline drives the real factory: an openrc's
// OS_VOLUME_API_VERSION=3 used to reach cinder as "volume 3", which it rejects
// with 400.
func TestVolume_MajorOnlyVersionSendsBaseline(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("OpenStack-API-Version")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &Client{
		Provider: &gophercloud.ProviderClient{
			TokenID:         "token",
			HTTPClient:      *srv.Client(),
			EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return srv.URL + "/", nil },
		},
		opts: &Options{VolumeAPIVersion: "3"},
	}
	sc, err := c.Volume()
	if err != nil {
		t.Fatalf("Volume: %v", err)
	}
	if sc.Microversion != "3.0" {
		t.Errorf("Microversion = %q, want %q", sc.Microversion, "3.0")
	}
	resp, err := sc.Get(context.Background(), sc.ServiceURL("volumes"), nil, nil)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if got != "volume 3.0" {
		t.Errorf("OpenStack-API-Version = %q, want %q", got, "volume 3.0")
	}
}
