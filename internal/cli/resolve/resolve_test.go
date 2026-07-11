package resolve

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"
)

func TestIsUUID(t *testing.T) {
	cases := map[string]bool{
		"11111111-1111-1111-1111-111111111111": true,
		"ABCDEF01-1111-1111-1111-111111111111": true,
		"not-a-uuid":                           false,
		"ubuntu-22.04":                         false,
		"":                                     false,
	}
	for in, want := range cases {
		if got := IsUUID(in); got != want {
			t.Errorf("IsUUID(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestImageID_UUIDPassthroughSkipsAPI(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	called := false
	fakeServer.Mux.HandleFunc("/images", func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	client := imageFakeClient(fakeServer)
	id, err := ImageID(context.Background(), client, "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if id != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("UUID should pass through unchanged, got %q", id)
	}
	if called {
		t.Error("a UUID reference must not trigger an API call")
	}
}

func TestImageID_NameResolves(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"images":[{"id":"img-123","name":"ubuntu"}]}`))
	})

	client := imageFakeClient(fakeServer)
	id, err := ImageID(context.Background(), client, "ubuntu")
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "ubuntu" {
		t.Errorf("name filter = %q, want ubuntu", gotQuery)
	}
	if id != "img-123" {
		t.Errorf("resolved id = %q, want img-123", id)
	}
}

func TestImageID_MultipleMatchesError(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/images", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"images":[{"id":"a","name":"dup"},{"id":"b","name":"dup"}]}`))
	})

	client := imageFakeClient(fakeServer)
	_, err := ImageID(context.Background(), client, "dup")
	if err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Errorf("expected a multiple-match error, got %v", err)
	}
}

func TestImageID_NoMatchFallsBackToRef(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/images", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"images":[]}`))
	})

	client := imageFakeClient(fakeServer)
	id, err := ImageID(context.Background(), client, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if id != "missing" {
		t.Errorf("no match should pass the reference through, got %q", id)
	}
}

func TestNetworkID_NameResolves(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v2.0/networks", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "private" {
			t.Errorf("name filter = %q, want private", r.URL.Query().Get("name"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"networks":[{"id":"net-9","name":"private"}]}`))
	})

	client := netFakeClient(fakeServer)
	id, err := NetworkID(context.Background(), client, "private")
	if err != nil {
		t.Fatal(err)
	}
	if id != "net-9" {
		t.Errorf("resolved id = %q, want net-9", id)
	}
}

func TestProjectID_NameResolves(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "demo" {
			t.Errorf("name filter = %q, want demo", r.URL.Query().Get("name"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"projects":[{"id":"proj-7","name":"demo"}]}`))
	})

	client := projectFakeClient(fakeServer)
	id, err := ProjectID(context.Background(), client, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if id != "proj-7" {
		t.Errorf("resolved id = %q, want proj-7", id)
	}
}

// Service clients pointed at the mock, matching the ResourceBase each service
// constructor applies (glance/keystone at root, neutron under /v2.0/).
func imageFakeClient(fakeServer th.FakeServer) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "image"
	return sc
}

func projectFakeClient(fakeServer th.FakeServer) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "identity"
	return sc
}

func netFakeClient(fakeServer th.FakeServer) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "network"
	sc.ResourceBase = sc.Endpoint + "v2.0/"
	return sc
}
