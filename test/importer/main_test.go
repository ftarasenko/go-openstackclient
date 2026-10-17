// Package importer_test builds a Session from FromProvider against an
// httptest cloud and lists images and networks through pkg/, with nothing but
// the module's public import paths.
package importer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/image"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

func cloud(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	answer := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /image/v2/images", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"images": []map[string]string{
			{"id": "0b1c2d3e-4f5a-4b6c-8d7e-9f0a1b2c3d4e", "name": "cirros", "status": "active"},
		}})
	})
	mux.HandleFunc("GET /network/v2.0/networks", func(w http.ResponseWriter, r *http.Request) {
		nets := []map[string]string{
			{"id": "9b5c1c2e-3f4a-4b6c-8d7e-0f1a2b3c4d5e", "name": "private"},
			{"id": "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", "name": "public"},
		}
		if name := r.URL.Query().Get("name"); name != "" {
			nets = nets[:1]
		}
		answer(w, map[string]any{"networks": nets})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestPublicAPIFromAnotherModule(t *testing.T) {
	srv := cloud(t)
	pc := &gophercloud.ProviderClient{
		TokenID: "importer-token",
		EndpointLocator: func(eo gophercloud.EndpointOpts) (string, error) {
			return srv.URL + "/" + eo.Type + "/", nil
		},
	}
	s := openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{})
	ctx := context.Background()

	imageClient, err := s.Image()
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := image.ListImages(ctx, imageClient, images.ListOpts{})
	if err != nil || len(imgs) != 1 || imgs[0].Name != "cirros" {
		t.Fatalf("ListImages = %v, %v", imgs, err)
	}

	networkClient, err := s.Network()
	if err != nil {
		t.Fatal(err)
	}
	nets, err := paging.Collect(ctx, networks.List(networkClient, networks.ListOpts{}), 0, networks.ExtractNetworks)
	if err != nil || len(nets) != 2 {
		t.Fatalf("networks = %v, %v", nets, err)
	}
	id, err := resolve.NetworkID(ctx, networkClient, "private")
	if err != nil || id != nets[0].ID {
		t.Fatalf("NetworkID = %q, %v", id, err)
	}
}
