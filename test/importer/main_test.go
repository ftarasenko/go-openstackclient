// Package importer_test builds a Session from FromProvider against an
// httptest cloud and lists images, networks, flavors, projects, role
// assignments and volumes through pkg/, with nothing but the module's public
// import paths.
package importer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/backups"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servergroups"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/services"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/roles"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/compute"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/identity"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/image"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/volume"
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
	mux.HandleFunc("GET /compute/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"flavors": []map[string]any{
			{"id": "1", "name": "m1.small", "os-flavor-access:is_public": true},
		}})
	})
	mux.HandleFunc("GET /compute/os-services", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"services": []map[string]any{
			{"id": "svc-1", "binary": "nova-compute", "host": "cmp-01", "status": "enabled", "admin_state": "Active"},
		}})
	})
	mux.HandleFunc("POST /compute/servers/srv-1/action", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /compute/servers/srv-1", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"server": map[string]any{"id": "srv-1", "status": "ACTIVE"}})
	})
	mux.HandleFunc("GET /compute/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("deleted") != "false" {
			http.Error(w, "want deleted=false", http.StatusBadRequest)
			return
		}
		answer(w, map[string]any{"servers": []map[string]any{{"id": "srv-1", "name": "web"}}})
	})
	mux.HandleFunc("POST /compute/servers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"server":{"id":"srv-1"}}`))
	})
	mux.HandleFunc("GET /compute/os-migrations", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"migrations": []map[string]any{{"id": 1, "status": "completed"}}})
	})
	mux.HandleFunc("GET /compute/os-server-groups", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all_projects") != "true" {
			http.Error(w, "want all_projects", http.StatusBadRequest)
			return
		}
		answer(w, map[string]any{"server_groups": []map[string]any{{"id": "grp-1", "name": "web", "policies": []string{"affinity"}}}})
	})
	mux.HandleFunc("GET /compute/servers/srv-1/os-instance-actions", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"instanceActions": []map[string]any{{"action": "create", "request_id": "req-1"}}})
	})
	mux.HandleFunc("GET /compute/servers/srv-1/os-volume_attachments", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"volumeAttachments": []map[string]any{{"device": "/dev/vdb", "volumeId": "vol-1", "attachment_id": "att-1"}}})
	})
	mux.HandleFunc("GET /identity/v3/projects", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("domain_id") != "default" {
			http.Error(w, "want domain_id", http.StatusBadRequest)
			return
		}
		answer(w, map[string]any{"projects": []map[string]any{{"id": "prj-1", "name": "web", "domain_id": "default"}}})
	})
	mux.HandleFunc("PUT /identity/v3/groups/grp-1/users/usr-1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /identity/v3/role_assignments", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("user.id") != "usr-1" {
			http.Error(w, "want user.id", http.StatusBadRequest)
			return
		}
		answer(w, map[string]any{"role_assignments": []map[string]any{{"role": map[string]any{"id": "rol-1"},
			"user": map[string]any{"id": "usr-1"}, "scope": map[string]any{"project": map[string]any{"id": "prj-1"}, "OS-INHERIT:inherited_to": "projects"}}}})
	})
	mux.HandleFunc("GET /block-storage/volumes/detail", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("user_id") != "usr-1" {
			http.Error(w, "want user_id", http.StatusBadRequest)
			return
		}
		answer(w, map[string]any{"volumes": []map[string]any{{"id": "vol-1", "name": "data", "status": "available"}}})
	})
	mux.HandleFunc("GET /block-storage/volumes/vol-1", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"volume": map[string]any{"id": "vol-1", "status": "available", "volume_type": "ssd"}})
	})
	mux.HandleFunc("GET /block-storage/backups/detail", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("volume_id") != "vol-1" {
			http.Error(w, "want volume_id", http.StatusBadRequest)
			return
		}
		answer(w, map[string]any{"backups": []map[string]any{{"id": "bak-1", "volume_id": "vol-1", "status": "available"}}})
	})
	mux.HandleFunc("PUT /block-storage/backups/bak-1", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Backup *struct {
				Name string `json:"name"`
			} `json:"backup"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Backup == nil {
			http.Error(w, "want a backup object", http.StatusBadRequest)
			return
		}
		answer(w, map[string]any{"backup": map[string]any{"id": "bak-1", "name": body.Backup.Name}})
	})
	mux.HandleFunc("GET /block-storage/os-services", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"services": []map[string]any{{"binary": "cinder-volume", "host": "node-1@lvm", "backend_state": "up"}}})
	})
	mux.HandleFunc("GET /block-storage/scheduler-stats/get_pools", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("detail") != "True" {
			http.Error(w, "want detail=True", http.StatusBadRequest)
			return
		}
		answer(w, map[string]any{"pools": []map[string]any{{"name": "node-1@lvm#pool", "capabilities": map[string]any{"total_capacity_gb": 100}}}})
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

	computeClient, err := s.Compute()
	if err != nil {
		t.Fatal(err)
	}
	if fls, err := compute.ListFlavors(ctx, computeClient, flavors.ListOpts{}); err != nil || len(fls) != 1 {
		t.Fatalf("ListFlavors = %v, %v", fls, err)
	}
	if fl, err := compute.FindFlavor(ctx, computeClient, "m1.small"); err != nil || fl.ID != "1" {
		t.Fatalf("FindFlavor = %v, %v", fl, err)
	}
	svcs, ext, err := compute.ListServices(ctx, computeClient, services.ListOpts{})
	if err != nil || len(svcs) != 1 || ext[0].AdminState != "Active" {
		t.Fatalf("ListServices = %v, %v, %v", svcs, ext, err)
	}
	if id, err := compute.FindServiceID(ctx, computeClient, "cmp-01", "nova-compute"); err != nil || id != "svc-1" {
		t.Fatalf("FindServiceID = %q, %v", id, err)
	}
	if err := compute.StartServer(ctx, computeClient, "srv-1"); err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	if err := compute.WaitForServerStatus(ctx, computeClient, "srv-1", []string{"ACTIVE"}, time.Millisecond); err != nil {
		t.Fatalf("WaitForServerStatus: %v", err)
	}
	if err := compute.MigrateServer(ctx, computeClient, "srv-1", ""); err != nil {
		t.Fatalf("MigrateServer: %v", err)
	}
	if ms, err := compute.ListMigrations(ctx, computeClient, compute.MigrationListOpts{}, 0); err != nil || len(ms) != 1 {
		t.Fatalf("ListMigrations = %v, %v", ms, err)
	}
	list, err := compute.ListServers(ctx, computeClient, compute.ServerListOpts{
		ListOpts: servers.ListOpts{ChangesSince: "2026-01-01T00:00:00Z"}, ExcludeDeleted: true,
	}, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListServers = %v, %v", list, err)
	}
	created, err := compute.CreateServer(ctx, computeClient, compute.ServerCreateOptsExt{
		CreateOptsBuilder: servers.CreateOpts{Name: "web", FlavorRef: "1", UserData: compute.EncodeUserData([]byte("hostname\n"))},
		Host:              "cmp-01",
	}, nil)
	if err != nil || created.ID != "srv-1" {
		t.Fatalf("CreateServer = %v, %v", created, err)
	}
	if gs, err := compute.ListServerGroups(ctx, computeClient, servergroups.ListOpts{AllProjects: true}, 0); err != nil || len(gs) != 1 {
		t.Fatalf("ListServerGroups = %v, %v", gs, err)
	}
	if as, err := compute.ListInstanceActions(ctx, computeClient, "srv-1", compute.InstanceActionListOpts{}, 0); err != nil || len(as) != 1 {
		t.Fatalf("ListInstanceActions = %v, %v", as, err)
	}
	if vs, err := compute.ListVolumeAttachments(ctx, computeClient, "srv-1"); err != nil || len(vs) != 1 || vs[0].AttachmentID != "att-1" {
		t.Fatalf("ListVolumeAttachments = %v, %v", vs, err)
	}

	identityClient, err := s.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if ps, err := identity.ListProjects(ctx, identityClient, projects.ListOpts{DomainID: "default"}); err != nil || len(ps) != 1 {
		t.Fatalf("ListProjects = %v, %v", ps, err)
	}
	if err := identity.AddUserToGroup(ctx, identityClient, "grp-1", "usr-1"); err != nil {
		t.Fatal(err)
	}
	if as, err := identity.ListRoleAssignments(ctx, identityClient, roles.ListAssignmentsOpts{UserID: "usr-1"}); err != nil ||
		len(as) != 1 || as[0].Role.ID != "rol-1" || as[0].InheritedTo != "projects" {
		t.Fatalf("ListRoleAssignments = %+v, %v", as, err)
	}

	volumeClient, err := s.Volume()
	if err != nil {
		t.Fatal(err)
	}
	vols, err := volume.ListVolumes(ctx, volumeClient, volume.ListOpts{
		ListOptsBuilder: volumes.ListOpts{AllTenants: true}, UserID: "usr-1",
	}, 0)
	if err != nil || len(vols) != 1 || vols[0].ID != "vol-1" {
		t.Fatalf("ListVolumes = %+v, %v", vols, err)
	}
	if err := volume.WaitForRetype(ctx, volumeClient, "vol-1", "", "ssd", time.Millisecond); err != nil {
		t.Fatalf("WaitForRetype: %v", err)
	}
	baks, err := volume.ListBackups(ctx, volumeClient, backups.ListOpts{VolumeID: "vol-1"}, 0)
	if err != nil || len(baks) != 1 || baks[0].ID != "bak-1" {
		t.Fatalf("ListBackups = %+v, %v", baks, err)
	}
	name := "nightly"
	if b, err := volume.UpdateBackup(ctx, volumeClient, "bak-1", backups.UpdateOpts{Name: &name}); err != nil || b.Name != name {
		t.Fatalf("UpdateBackup = %+v, %v", b, err)
	}
	volSvcs, err := volume.ListServices(ctx, volumeClient, nil)
	if err != nil || len(volSvcs) != 1 || volSvcs[0].Binary != "cinder-volume" || volSvcs[0].BackendState != "up" {
		t.Fatalf("ListServices = %+v, %v", volSvcs, err)
	}
	pools, err := volume.ListStoragePools(ctx, volumeClient)
	if err != nil || len(pools) != 1 || fmt.Sprint(pools[0].Capabilities["total_capacity_gb"]) != "100" {
		t.Fatalf("ListStoragePools = %+v, %v", pools, err)
	}
}
