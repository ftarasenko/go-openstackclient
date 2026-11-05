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
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/extension"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/identity"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/image"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/network"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/ratelimit"
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
		nets := []map[string]any{
			{"id": "9b5c1c2e-3f4a-4b6c-8d7e-0f1a2b3c4d5e", "name": "private", "mtu": 1450},
			{"id": "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", "name": "public", "router:external": true},
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
	mux.HandleFunc("GET /block-storage/os-quota-sets/prj-1", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"quota_set": map[string]any{"volumes": 10, "gigabytes_ssd": 50}})
	})
	mux.HandleFunc("GET /block-storage/limits", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"limits": map[string]any{"rate": []map[string]any{{"uri": "*", "limit": []map[string]any{{"verb": "POST", "value": 10}}}}}})
	})
	mux.HandleFunc("GET /network/v2.0/quotas/prj-1", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"quota": map[string]any{"network": 10}})
	})
	mux.HandleFunc("GET /network/v2.0/floatingips", func(w http.ResponseWriter, r *http.Request) {
		fips := []map[string]any{}
		if r.URL.Query().Get("floating_network_id") == "9b5c1c2e-3f4a-4b6c-8d7e-0f1a2b3c4d5e" {
			fips = append(fips, map[string]any{"id": "fip-1", "floating_ip_address": "198.51.100.20", "qos_policy_id": "qos-1"})
		}
		answer(w, map[string]any{"floatingips": fips})
	})
	sg := map[string]any{"id": "sg-1", "name": "default", "shared": false,
		"security_group_rules": []map[string]any{{"id": "rule-1", "direction": "egress", "normalized_cidr": nil}}}
	mux.HandleFunc("GET /network/v2.0/security-groups", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"security_groups": []map[string]any{sg}})
	})
	mux.HandleFunc("GET /network/v2.0/security-groups/sg-1", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"security_group": sg})
	})
	mux.HandleFunc("GET /network/v2.0/routers/router-1", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"router": map[string]any{"id": "router-1", "name": "r1", "ha": true}})
	})
	mux.HandleFunc("GET /network/v2.0/ports", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"ports": []map[string]any{{"id": "port-1", "binding:vnic_type": "normal",
			"fixed_ips": []map[string]any{{"subnet_id": "sub-1", "ip_address": "192.0.2.1"}}}}})
	})
	mux.HandleFunc("GET /network/v2.0/extensions", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"extensions": []map[string]any{{"name": "Trunk", "alias": "trunk"}}})
	})
	mux.HandleFunc("GET /network/v2.0/subnetpools/sp-1", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"subnetpool": map[string]any{"id": "sp-1", "default_quota": nil,
			"default_prefixlen": 26, "min_prefixlen": 8, "max_prefixlen": 32}})
	})
	mux.HandleFunc("GET /network/v2.0/qos/policies/qos-1", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"policy": map[string]any{"id": "qos-1",
			"rules": []map[string]any{{"id": "qr-1", "type": "minimum_packet_rate"}}}})
	})
	mux.HandleFunc("GET /network/v2.0/network-ip-availabilities", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"network_ip_availabilities": []map[string]any{{"network_id": "net-1", "total_ips": 253, "used_ips": 3,
			"ip_availability_details": map[string]any{"used": 3}}}})
	})
	mux.HandleFunc("GET /network/v2.0/taas/tap_mirrors", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"tap_mirrors": []map[string]any{{"id": "tm-1", "directions": map[string]any{"IN": 101}}}})
	})
	mux.HandleFunc("GET /network/v2.0/routers/router-1/l3-agents", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"agents": []map[string]any{{"id": "agent-1", "ha_state": "active"}}})
	})
	mux.HandleFunc("GET /network/v2.0/bgpvpn/bgpvpns", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"bgpvpns": []map[string]any{{"id": "vpn-1", "type": "l3", "route_targets": []string{"64512:1"}}}})
	})
	mux.HandleFunc("GET /network/v2.0/fwaas/firewall_rules", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"firewall_rules": []map[string]any{{"id": "fwr-1", "action": "deny", "source_firewall_group_id": "fwg-1"}}})
	})
	mux.HandleFunc("GET /network/v2.0/vpn/vpnservices", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, map[string]any{"vpnservices": []map[string]any{{"id": "vpns-1", "router_id": "router-1"}}})
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
	vq, err := volume.GetQuotaSet(ctx, volumeClient, "prj-1")
	if err != nil || vq.Volumes != 10 || vq.PerVolumeType["gigabytes_ssd"] != 50 {
		t.Fatalf("volume.GetQuotaSet = %+v, %v", vq, err)
	}
	if rl, err := ratelimit.List(ctx, volumeClient, ""); err != nil || len(rl) != 1 || rl[0].Verb != "POST" {
		t.Fatalf("ratelimit.List = %+v, %v", rl, err)
	}
	if nq, err := network.GetQuota(ctx, networkClient, "prj-1"); err != nil || nq.Network != 10 {
		t.Fatalf("network.GetQuota = %+v, %v", nq, err)
	}
	if exts, err := extension.List(ctx, networkClient); err != nil || len(exts) != 1 || exts[0].Alias != "trunk" {
		t.Fatalf("extension.List = %+v, %v", exts, err)
	}
	if all, err := network.ListNetworks(ctx, networkClient, nil); err != nil || len(all) != 2 || all[0].MTU != 1450 || !all[1].External {
		t.Fatalf("network.ListNetworks = %+v, %v", all, err)
	}
	if r, err := network.GetRouter(ctx, networkClient, "router-1"); err != nil || r.Name != "r1" || r.Attrs.HA == nil || !*r.Attrs.HA {
		t.Fatalf("network.GetRouter = %+v, %v", r, err)
	}
	if ifaces, err := network.ListRouterInterfaces(ctx, networkClient, "router-1"); err != nil || len(ifaces) != 1 || ifaces[0].SubnetID != "sub-1" {
		t.Fatalf("network.ListRouterInterfaces = %+v, %v", ifaces, err)
	}
	if ps, err := network.ListPorts(ctx, networkClient, nil); err != nil || len(ps) != 1 || ps[0].BindingVNICType != "normal" {
		t.Fatalf("network.ListPorts = %+v, %v", ps, err)
	}
	netID, err := network.FindNetworkID(ctx, networkClient, "private")
	if err != nil {
		t.Fatalf("network.FindNetworkID: %v", err)
	}
	fips, err := network.ListFloatingIPs(ctx, networkClient, network.FloatingIPListOpts{NetworkIDs: []string{netID}})
	if err != nil || len(fips) != 1 || fips[0].QoSPolicyID != "qos-1" {
		t.Fatalf("network.ListFloatingIPs = %+v, %v", fips, err)
	}
	sgs, err := network.ListSecurityGroups(ctx, networkClient, network.SecurityGroupListOpts{})
	if err != nil || len(sgs) != 1 || len(sgs[0].Rules) != 1 || sgs[0].Shared == nil {
		t.Fatalf("network.ListSecurityGroups = %+v, %v", sgs, err)
	}
	if g, err := network.GetSecurityGroup(ctx, networkClient, "sg-1"); err != nil || len(g.RawRules) != 1 {
		t.Fatalf("network.GetSecurityGroup = %+v, %v", g, err)
	} else if _, ok := g.RawRules[0]["normalized_cidr"]; !ok {
		t.Fatalf("network.GetSecurityGroup dropped a rule key: %v", g.RawRules[0])
	}
	if p, err := network.GetSubnetPool(ctx, networkClient, "sp-1"); err != nil || p.Attrs.DefaultQuota != nil || p.DefaultPrefixLen != 26 {
		t.Fatalf("network.GetSubnetPool = %+v, %v", p, err)
	}
	if typ, err := network.FindQoSRuleType(ctx, networkClient, "qos-1", "qr-1"); err != nil || typ != "minimum_packet_rate" {
		t.Fatalf("network.FindQoSRuleType = %q, %v", typ, err)
	}
	if av, err := network.ListIPAvailabilities(ctx, networkClient, nil); err != nil || len(av) != 1 || av[0].Details["used"] != float64(3) {
		t.Fatalf("network.ListIPAvailabilities = %+v, %v", av, err)
	}
	if tm, err := network.ListTapMirrors(ctx, networkClient, nil); err != nil || len(tm) != 1 || tm[0].Directions["IN"] != float64(101) {
		t.Fatalf("network.ListTapMirrors = %+v, %v", tm, err)
	}
	if l3, err := network.ListRouterL3Agents(ctx, networkClient, "router-1"); err != nil || len(l3) != 1 || l3[0].HAState != "active" {
		t.Fatalf("network.ListRouterL3Agents = %+v, %v", l3, err)
	}
	if vpns, err := network.ListBGPVPNs(ctx, networkClient, nil); err != nil || len(vpns) != 1 || vpns[0].RouteTargets[0] != "64512:1" {
		t.Fatalf("network.ListBGPVPNs = %+v, %v", vpns, err)
	}
	if fr, err := network.ListFirewallRules(ctx, networkClient, nil); err != nil || len(fr) != 1 || fr[0].Action != "deny" || fr[0].SourceFirewallGroupID != "fwg-1" {
		t.Fatalf("network.ListFirewallRules = %+v, %v", fr, err)
	}
	if svcs, err := network.ListVPNServices(ctx, networkClient, nil); err != nil || len(svcs) != 1 || svcs[0].RouterID != "router-1" {
		t.Fatalf("network.ListVPNServices = %+v, %v", svcs, err)
	}
}
