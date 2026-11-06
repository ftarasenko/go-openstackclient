package volume_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/backups"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/services"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/transfers"

	"github.com/ftarasenko/koc/pkg/openstack/volume"
)

const trID = "3e2d1c0b-9a8f-4e7d-8c6b-5a4f3e2d1c0b"

func TestTransfers(t *testing.T) {
	tr := `{"transfer":{"id":"` + trID + `","name":"move","volume_id":"` + volID + `","auth_key":"k3y","created_at":"2026-01-02T03:04:05.000000"}}`
	c, sc := newCinder(t, "3.70", map[string]route{
		"GET /os-volume-transfer/detail": func(r *http.Request) (int, string) {
			if r.URL.Query().Get("marker") == "" {
				return 200, `{"transfers":[{"id":"a"},{"id":"b"}],
					"transfers_links":[{"rel":"next","href":"http://` + r.Host + `/os-volume-transfer/detail?marker=b"}]}`
			}
			return 200, `{"transfers":[{"id":"c"}]}`
		},
		"POST /os-volume-transfer":                     fixed(202, tr),
		"POST /volume-transfers":                       fixed(202, tr),
		"GET /os-volume-transfer/" + trID:              fixed(200, tr),
		"DELETE /os-volume-transfer/" + trID:           fixed(202, ``),
		"POST /os-volume-transfer/" + trID + "/accept": fixed(202, tr),
	})
	ctx := context.Background()

	all, err := volume.ListTransfers(ctx, sc, transfers.ListOpts{AllTenants: true}, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListTransfers = %d, %v; want 3 across two pages", len(all), err)
	}
	if got := c.take(); got[0].Query != "all_tenants=true" {
		t.Errorf("query = %q", got[0].Query)
	}
	if capped, err := volume.ListTransfers(ctx, sc, transfers.ListOpts{}, 1); err != nil || len(capped) != 1 {
		t.Fatalf("capped = %+v, %v", capped, err)
	}
	c.take()

	got, err := volume.CreateTransfer(ctx, sc, transfers.CreateOpts{VolumeID: volID, Name: "move"})
	if err != nil || got.AuthKey != "k3y" {
		t.Fatalf("CreateTransfer = %+v, %v", got, err)
	}
	c.want(t, http.MethodPost, "/os-volume-transfer", "", `{"transfer":{"name":"move","volume_id":"`+volID+`"}}`)

	got, err = volume.CreateTransferWithoutSnapshots(ctx, sc, volID, "move")
	if err != nil || got.ID != trID || got.AuthKey != "k3y" {
		t.Fatalf("CreateTransferWithoutSnapshots = %+v, %v", got, err)
	}
	r := c.only(t)
	if r.Path != "/volume-transfers" || r.Version != "volume "+volume.TransferSnapshotsMicroversion ||
		r.Body != `{"transfer":{"name":"move","no_snapshots":true,"volume_id":"`+volID+`"}}` {
		t.Errorf("request = %+v; want the newer endpoint at 3.55", r)
	}
	if sc.Microversion != "3.70" {
		t.Errorf("the caller's client was re-pinned to %s", sc.Microversion)
	}
	if _, err := volume.CreateTransferWithoutSnapshots(ctx, sc, volID, ""); err != nil {
		t.Fatal(err)
	}
	if r := c.only(t); r.Body != `{"transfer":{"no_snapshots":true,"volume_id":"`+volID+`"}}` {
		t.Errorf("unnamed body = %s; want no name key", r.Body)
	}

	if got, err := volume.GetTransfer(ctx, sc, trID); err != nil || got.Name != "move" {
		t.Fatalf("GetTransfer = %+v, %v", got, err)
	}
	c.want(t, http.MethodGet, "/os-volume-transfer/"+trID, "", "")

	if _, err := volume.AcceptTransfer(ctx, sc, trID, "k3y"); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodPost, "/os-volume-transfer/"+trID+"/accept", "", `{"accept":{"auth_key":"k3y"}}`)

	if err := volume.DeleteTransfer(ctx, sc, trID); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodDelete, "/os-volume-transfer/"+trID, "", "")
}

func TestServices(t *testing.T) {
	c, sc := newCinder(t, "3.70", map[string]route{
		"GET /os-services": fixed(200, `{"services":[
			{"binary":"cinder-volume","host":"node-1@lvm","zone":"nova","status":"enabled","state":"up","backend_state":"up","cluster":"c1"},
			{"binary":"cinder-scheduler","host":"node-1","zone":"nova","status":"disabled","state":"up","disabled_reason":"maint"}]}`),
		"PUT /os-services/enable":             fixed(200, `{}`),
		"PUT /os-services/disable":            fixed(200, `{}`),
		"PUT /os-services/disable-log-reason": fixed(200, `{}`),
	})
	ctx := context.Background()

	all, err := volume.ListServices(ctx, sc, services.ListOpts{Host: "node-1@lvm", Binary: "cinder-volume"})
	if err != nil || len(all) != 2 {
		t.Fatalf("ListServices = %+v, %v", all, err)
	}
	if all[0].BackendState != "up" || all[0].Cluster != "c1" || all[1].BackendState != "" || all[1].DisabledReason != "maint" {
		t.Errorf("services = %+v; want backend_state aligned with each service", all)
	}
	c.want(t, http.MethodGet, "/os-services", "binary=cinder-volume&host=node-1%40lvm", "")

	for _, tc := range []struct {
		opts       volume.ServiceUpdateOpts
		path, body string
	}{
		{volume.ServiceUpdateOpts{Host: "h", Binary: "b"}, "/os-services/enable", `{"binary":"b","host":"h"}`},
		{volume.ServiceUpdateOpts{Host: "h", Binary: "b", Disabled: true}, "/os-services/disable", `{"binary":"b","host":"h"}`},
		{volume.ServiceUpdateOpts{Host: "h", Binary: "b", DisabledReason: "maint"}, "/os-services/disable-log-reason",
			`{"binary":"b","disabled_reason":"maint","host":"h"}`},
	} {
		if err := volume.UpdateService(ctx, sc, tc.opts); err != nil {
			t.Fatal(err)
		}
		c.want(t, http.MethodPut, tc.path, "", tc.body)
	}
}

func TestStoragePoolsAndCapabilities(t *testing.T) {
	c, sc := newCinder(t, "3.70", map[string]route{
		"GET /scheduler-stats/get_pools": fixed(200, `{"pools":[{"name":"node-1@lvm#pool","capabilities":{"total_capacity_gb":100,"free_capacity_gb":"infinite"}}]}`),
		"GET /capabilities/node-1@lvm":   fixed(200, `{"vendor_name":"Open Source","properties":{"thin":{"type":"boolean"}}}`),
	})
	ctx := context.Background()

	pools, err := volume.ListStoragePools(ctx, sc)
	if err != nil || len(pools) != 1 || pools[0].Capabilities["free_capacity_gb"] != "infinite" {
		t.Fatalf("ListStoragePools = %+v, %v", pools, err)
	}
	c.want(t, http.MethodGet, "/scheduler-stats/get_pools", "detail=True", "")

	caps, err := volume.GetBackendCapabilities(ctx, sc, "node-1@lvm")
	if err != nil || caps["vendor_name"] != "Open Source" {
		t.Fatalf("GetBackendCapabilities = %v, %v", caps, err)
	}
	c.want(t, http.MethodGet, "/capabilities/node-1@lvm", "", "")
}

func TestClusters(t *testing.T) {
	cl := `{"cluster":{"name":"c1@lvm","binary":"cinder-volume","state":"up","status":"disabled","disabled_reason":"maint","num_hosts":2,"frozen":true}}`
	c, sc := newCinder(t, "3.70", map[string]route{
		"GET /clusters":         fixed(200, `{"clusters":[{"name":"c1@lvm","binary":"cinder-volume","state":"up","status":"enabled"}]}`),
		"GET /clusters/detail":  fixed(200, `{"clusters":[{"name":"c1@lvm","num_hosts":2,"num_down_hosts":1}]}`),
		"GET /clusters/c1@lvm":  fixed(200, cl),
		"PUT /clusters/enable":  fixed(200, cl),
		"PUT /clusters/disable": fixed(200, cl),
	})
	ctx := context.Background()
	up, disabled, hosts, down := true, false, 2, 0

	all, err := volume.ListClusters(ctx, sc, volume.ClusterListOpts{})
	if err != nil || len(all) != 1 || all[0].Status != "enabled" {
		t.Fatalf("ListClusters = %+v, %v", all, err)
	}
	c.want(t, http.MethodGet, "/clusters", "", "")

	all, err = volume.ListClusters(ctx, sc, volume.ClusterListOpts{
		Name: "c1@lvm", Binary: "cinder-volume", IsUp: &up, Disabled: &disabled, NumHosts: &hosts, NumDownHosts: &down, Detail: true,
	})
	if err != nil || all[0].NumDownHosts != 1 {
		t.Fatalf("ListClusters(detail) = %+v, %v", all, err)
	}
	c.want(t, http.MethodGet, "/clusters/detail",
		"binary=cinder-volume&disabled=false&is_up=true&name=c1%40lvm&num_down_hosts=0&num_hosts=2", "")

	got, err := volume.GetCluster(ctx, sc, "c1@lvm", "")
	if err != nil || !got.Frozen || got.NumHosts != 2 {
		t.Fatalf("GetCluster = %+v, %v", got, err)
	}
	c.want(t, http.MethodGet, "/clusters/c1@lvm", "", "")
	if _, err := volume.GetCluster(ctx, sc, "c1@lvm", "cinder-volume"); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodGet, "/clusters/c1@lvm", "binary=cinder-volume", "")

	if got, err := volume.EnableCluster(ctx, sc, "c1@lvm", ""); err != nil || got.Name != "c1@lvm" {
		t.Fatalf("EnableCluster = %+v, %v", got, err)
	}
	c.want(t, http.MethodPut, "/clusters/enable", "", `{"name":"c1@lvm"}`)

	if got, err := volume.DisableCluster(ctx, sc, "c1@lvm", "cinder-volume", "maint"); err != nil || got.DisabledReason != "maint" {
		t.Fatalf("DisableCluster = %+v, %v", got, err)
	}
	c.want(t, http.MethodPut, "/clusters/disable", "", `{"binary":"cinder-volume","disabled_reason":"maint","name":"c1@lvm"}`)
}

func TestListBackupSummaries(t *testing.T) {
	c, sc := newCinder(t, "3.70", map[string]route{
		"GET /backups": fixed(200, `{"backups":[{"id":"`+bakID+`","name":"b1","links":[]}]}`),
	})
	all, err := volume.ListBackupSummaries(context.Background(), sc, backups.ListOpts{Name: "b1"}, 0)
	if err != nil || len(all) != 1 || all[0].ID != bakID {
		t.Fatalf("ListBackupSummaries = %+v, %v", all, err)
	}
	c.want(t, http.MethodGet, "/backups", "name=b1", "")
}

// Every call names what it was doing, and keeps gophercloud's 404 reachable.
func TestOpsErrorsNameTheCall(t *testing.T) {
	_, sc := newCinder(t, "3.70", nil)
	ctx := context.Background()
	for _, tc := range []struct {
		want string
		err  error
	}{
		{"listing volume transfer requests: ", second(volume.ListTransfers(ctx, sc, transfers.ListOpts{}, 0))},
		{"showing volume transfer request " + trID + ": ", second(volume.GetTransfer(ctx, sc, trID))},
		{"creating a transfer request for volume " + volID + ": ", second(volume.CreateTransfer(ctx, sc, transfers.CreateOpts{VolumeID: volID}))},
		{"creating a transfer request for volume " + volID + ": ", second(volume.CreateTransferWithoutSnapshots(ctx, sc, volID, ""))},
		{"deleting volume transfer request " + trID + ": ", volume.DeleteTransfer(ctx, sc, trID)},
		{"accepting volume transfer request " + trID + ": ", second(volume.AcceptTransfer(ctx, sc, trID, "k"))},
		{"listing volume services: ", second(volume.ListServices(ctx, sc, services.ListOpts{}))},
		{"setting block storage service h/b: ", volume.UpdateService(ctx, sc, volume.ServiceUpdateOpts{Host: "h", Binary: "b"})},
		{"listing storage pools: ", second(volume.ListStoragePools(ctx, sc))},
		{"showing capabilities of backend h@b: ", second(volume.GetBackendCapabilities(ctx, sc, "h@b"))},
		{"listing block storage clusters: ", second(volume.ListClusters(ctx, sc, volume.ClusterListOpts{}))},
		{`showing block storage cluster "c1": `, second(volume.GetCluster(ctx, sc, "c1", ""))},
		{`enabling block storage cluster "c1": `, second(volume.EnableCluster(ctx, sc, "c1", ""))},
		{`disabling block storage cluster "c1": `, second(volume.DisableCluster(ctx, sc, "c1", "", ""))},
		{"listing backups: ", second(volume.ListBackupSummaries(ctx, sc, backups.ListOpts{}, 0))},
	} {
		if tc.err == nil || !strings.HasPrefix(tc.err.Error(), tc.want) {
			t.Errorf("err = %v; want prefix %q", tc.err, tc.want)
			continue
		}
		if !gophercloud.ResponseCodeIs(tc.err, http.StatusNotFound) {
			t.Errorf("%q: gophercloud's 404 is not reachable", tc.want)
		}
	}
}

func TestListServices_MalformedReply(t *testing.T) {
	_, sc := newCinder(t, "3.70", map[string]route{
		"GET /os-services": fixed(200, `{"services":[{"binary":1}]}`),
	})
	if _, err := volume.ListServices(context.Background(), sc, services.ListOpts{}); err == nil ||
		!strings.HasPrefix(err.Error(), "listing volume services: ") {
		t.Fatalf("err = %v; want the listing named", err)
	}
}
