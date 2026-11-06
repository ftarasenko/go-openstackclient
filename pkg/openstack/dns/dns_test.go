package dns_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/quotas"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/recordsets"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/transfer/request"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/tsigkeys"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/zones"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/koc/pkg/openstack/dns"
)

const (
	zoneID = "a86dba58-0043-4cc6-a1bb-69d5e86f3ca3"
	rsID   = "f7b10e9b-0cae-4a91-b162-562bc6096648"
	objID  = "6d7c4c0a-3f3d-4f16-9f63-2f0f6b2b8f61"
	prj    = "5f0c1d2e3a4b4c5d8e9f0a1b2c3d4e5f"
	ptrID  = "RegionOne:" + objID

	zoneJSON = `{"id":"` + zoneID + `","name":"example.com.","email":"a@example.com","ttl":3600,"shared":true}`
	rsJSON   = `{"id":"` + rsID + `","zone_id":"` + zoneID + `","name":"www.example.com.","type":"A","records":["192.0.2.1"],"ttl":null}`
	idJSON   = `{"id":"` + objID + `"}`

	noBody = "-"
)

// request is what the fake endpoint expects of the one call it answers.
type expect struct {
	method string
	path   string // path, with ?query when one is sent
	body   string // JSON body; "" skips the check, noBody wants none
	header map[string]string
}

// endpoint answers one request with status and resp after checking it
// against want, and counts the calls.
func endpoint(t *testing.T, want expect, status int, resp string) (*gophercloud.ServiceClient, *int) {
	t.Helper()
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	calls := 0
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		calls++
		checkRequest(t, r, want)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	})
	sc := fakeclient.ServiceClient(fs)
	sc.Type = "dns"
	sc.ResourceBase = sc.Endpoint + "v2/"
	return sc, &calls
}

func checkRequest(t *testing.T, r *http.Request, want expect) {
	t.Helper()
	if r.Method != want.method {
		t.Errorf("method = %s; want %s", r.Method, want.method)
	}
	path, rawQuery, _ := strings.Cut(want.path, "?")
	if r.URL.Path != path {
		t.Errorf("path = %s; want %s", r.URL.Path, path)
	}
	wantQuery, _ := url.ParseQuery(rawQuery)
	if got := r.URL.Query(); !reflect.DeepEqual(got, wantQuery) && (len(got) > 0 || len(wantQuery) > 0) {
		t.Errorf("query = %v; want %v", got, wantQuery)
	}
	for k, v := range want.header {
		if got := r.Header.Get(k); got != v {
			t.Errorf("header %s = %q; want %q", k, got, v)
		}
	}
	switch want.body {
	case "":
	case noBody:
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			t.Errorf("body = %s; want none", b)
		}
	default:
		th.TestJSONRequest(t, r, want.body)
	}
}

type endpointCase struct {
	name   string
	want   expect
	status int
	resp   string
	call   func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error
	errPfx string // what the error starts with when designate answers 500
}

func ptr[T any](v T) *T { return &v }

func zoneCases() []endpointCase {
	return []endpointCase{
		{"ListZones", expect{method: "GET", path: "/v2/zones?name=example.com."}, 200, `{"zones":[` + zoneJSON + `],"links":{}}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := dns.ListZones(ctx, c, zones.ListOpts{Name: "example.com."}, 0)
				if err == nil && (len(all) != 1 || all[0].ID != zoneID) {
					t.Errorf("zones = %+v", all)
				}
				return err
			}, "listing dns zones: "},
		{"GetZone", expect{method: "GET", path: "/v2/zones/" + zoneID}, 200, zoneJSON,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				z, err := dns.GetZone(ctx, c, zoneID)
				if err == nil && (z.Name != "example.com." || z.Shared == nil || !*z.Shared) {
					t.Errorf("zone = %+v shared %v", z.Zone, z.Shared)
				}
				return err
			}, "getting dns zone " + zoneID + ": "},
		{"CreateZone", expect{method: "POST", path: "/v2/zones", body: `{"name":"example.com.","email":"a@example.com"}`}, 201, zoneJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.CreateZone(ctx, c, zones.CreateOpts{Name: "example.com.", Email: "a@example.com"})
				return err
			}, "creating dns zone: "},
		{"UpdateZone zero TTL", expect{method: "PATCH", path: "/v2/zones/" + zoneID, body: `{"ttl":0}`}, 202, zoneJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.UpdateZone(ctx, c, zoneID, dns.ZoneUpdateMap{"ttl": 0})
				return err
			}, "updating dns zone " + zoneID + ": "},
		{"DeleteZone", expect{method: "DELETE", path: "/v2/zones/" + zoneID}, 202, zoneJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return dns.DeleteZone(ctx, c, zoneID)
			}, "deleting dns zone " + zoneID + ": "},
		{"ZoneTask without body", expect{method: "POST", path: "/v2/zones/" + zoneID + "/tasks/abandon", body: noBody}, 204, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return dns.ZoneTask(ctx, c, zoneID, "abandon", nil)
			}, "abandon zone " + zoneID + ": "},
		{"ZoneTask with body", expect{method: "POST", path: "/v2/zones/" + zoneID + "/tasks/pool_move", body: `{"pool_id":"` + objID + `"}`}, 202, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return dns.ZoneTask(ctx, c, zoneID, "pool_move", map[string]any{"pool_id": objID})
			}, "pool_move zone " + zoneID + ": "},
		{"ListZoneNameservers", expect{method: "GET", path: "/v2/zones/" + zoneID + "/nameservers"}, 200,
			`{"nameservers":[{"hostname":"ns1.example.com.","priority":1}]}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				ns, err := dns.ListZoneNameservers(ctx, c, zoneID)
				if err == nil && !reflect.DeepEqual(ns, []dns.PoolNS{{Hostname: "ns1.example.com.", Priority: 1}}) {
					t.Errorf("nameservers = %+v", ns)
				}
				return err
			}, "listing nameservers of zone " + zoneID + ": "},
		{"ShareZone", expect{method: "POST", path: "/v2/zones/" + zoneID + "/shares", body: `{"target_project_id":"` + prj + `"}`}, 201, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.ShareZone(ctx, c, zoneID, prj)
				return err
			}, "sharing zone " + zoneID + ` with project "` + prj + `": `},
		{"ListZoneShares", expect{method: "GET", path: "/v2/zones/" + zoneID + "/shares"}, 200, `{"shared_zones":[` + idJSON + `],"links":{}}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := dns.ListZoneShares(ctx, c, zoneID)
				if err == nil && len(all) != 1 {
					t.Errorf("shares = %+v", all)
				}
				return err
			}, "listing shares of zone " + zoneID + ": "},
		{"GetZoneShare", expect{method: "GET", path: "/v2/zones/" + zoneID + "/shares/" + objID}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.GetZoneShare(ctx, c, zoneID, objID)
				return err
			}, "showing share " + objID + " of zone " + zoneID + ": "},
		{"UnshareZone", expect{method: "DELETE", path: "/v2/zones/" + zoneID + "/shares/" + objID}, 204, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return dns.UnshareZone(ctx, c, zoneID, objID)
			}, "removing share " + objID + " from zone " + zoneID + ": "},
	}
}

func recordSetCases() []endpointCase {
	base := "/v2/zones/" + zoneID + "/recordsets"
	return []endpointCase{
		{"ListRecordSets", expect{method: "GET", path: base + "?type=A"}, 200,
			`{"recordsets":[` + rsJSON + `,{"id":"` + objID + `","name":"mail.example.com.","type":"A","ttl":300}],"links":{}}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := dns.ListRecordSets(ctx, c, zoneID, recordsets.ListOpts{Type: "A"}, 0)
				if err == nil && (len(all) != 2 || all[0].NullableTTL != nil || all[1].NullableTTL == nil || *all[1].NullableTTL != 300) {
					t.Errorf("recordsets = %+v", all)
				}
				return err
			}, "listing recordsets in zone " + zoneID + ": "},
		{"GetRecordSet", expect{method: "GET", path: base + "/" + rsID}, 200, rsJSON,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				rs, err := dns.GetRecordSet(ctx, c, zoneID, rsID)
				if err == nil && (rs.Name != "www.example.com." || rs.NullableTTL != nil) {
					t.Errorf("recordset = %+v", rs)
				}
				return err
			}, "getting recordset " + rsID + ": "},
		{"CreateRecordSet", expect{method: "POST", path: base, body: `{"name":"www.example.com.","type":"A","records":["192.0.2.1"]}`}, 201, rsJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.CreateRecordSet(ctx, c, zoneID, recordsets.CreateOpts{Name: "www.example.com.", Type: "A", Records: []string{"192.0.2.1"}})
				return err
			}, "creating recordset in zone " + zoneID + ": "},
		{"UpdateRecordSet", expect{method: "PUT", path: base + "/" + rsID, body: `{"records":["192.0.2.2"],"ttl":60}`}, 202, rsJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.UpdateRecordSet(ctx, c, zoneID, rsID, recordsets.UpdateOpts{Records: []string{"192.0.2.2"}, TTL: ptr(60)})
				return err
			}, "updating recordset " + rsID + ": "},
		{"DeleteRecordSet", expect{method: "DELETE", path: base + "/" + rsID}, 202, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return dns.DeleteRecordSet(ctx, c, zoneID, rsID)
			}, "deleting recordset " + rsID + ": "},
	}
}

func transferCases() []endpointCase {
	reqs := "/v2/zones/tasks/transfer_requests"
	accepts := "/v2/zones/tasks/transfer_accepts"
	return []endpointCase{
		{"CreateTransferRequest", expect{method: "POST", path: "/v2/zones/" + zoneID + "/tasks/transfer_requests", body: `{"target_project_id":"` + prj + `"}`}, 201, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.CreateTransferRequest(ctx, c, zoneID, request.CreateOpts{TargetProjectID: prj})
				return err
			}, "creating transfer request for zone " + zoneID + ": "},
		{"ListTransferRequests", expect{method: "GET", path: reqs}, 200, `{"transfer_requests":[` + idJSON + `],"links":{}}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := dns.ListTransferRequests(ctx, c, request.ListOpts{})
				if err == nil && len(all) != 1 {
					t.Errorf("requests = %+v", all)
				}
				return err
			}, "listing zone transfer requests: "},
		{"GetTransferRequest", expect{method: "GET", path: reqs + "/" + objID}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.GetTransferRequest(ctx, c, objID)
				return err
			}, "showing zone transfer request " + objID + ": "},
		{"UpdateTransferRequest", expect{method: "PATCH", path: reqs + "/" + objID, body: `{"description":"d"}`}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.UpdateTransferRequest(ctx, c, objID, request.UpdateOpts{Description: "d"})
				return err
			}, "updating zone transfer request " + objID + ": "},
		{"DeleteTransferRequest", expect{method: "DELETE", path: reqs + "/" + objID}, 204, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return dns.DeleteTransferRequest(ctx, c, objID)
			}, "deleting zone transfer request " + objID + ": "},
		{"AcceptTransferRequest", expect{method: "POST", path: accepts, body: `{"key":"k","zone_transfer_request_id":"` + objID + `"}`}, 201, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.AcceptTransferRequest(ctx, c, objID, "k")
				return err
			}, "accepting zone transfer request " + objID + ": "},
		{"ListTransferAccepts", expect{method: "GET", path: accepts}, 200, `{"transfer_accepts":[` + idJSON + `],"links":{}}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := dns.ListTransferAccepts(ctx, c, nil)
				if err == nil && len(all) != 1 {
					t.Errorf("accepts = %+v", all)
				}
				return err
			}, "listing zone transfer accepts: "},
		{"GetTransferAccept", expect{method: "GET", path: accepts + "/" + objID}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.GetTransferAccept(ctx, c, objID)
				return err
			}, "showing zone transfer accept " + objID + ": "},
	}
}

func quotaCases() []endpointCase {
	return []endpointCase{
		{"GetQuota", expect{method: "GET", path: "/v2/quotas/" + prj}, 200, `{"zones":10}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				q, err := dns.GetQuota(ctx, c, prj)
				if err == nil && q.Zones != 10 {
					t.Errorf("quota = %+v", q)
				}
				return err
			}, `showing DNS quotas for project "` + prj + `": `},
		{"UpdateQuota", expect{method: "PATCH", path: "/v2/quotas/" + prj, body: `{"zones":3}`}, 200, `{"zones":3}`,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.UpdateQuota(ctx, c, prj, quotas.UpdateOpts{Zones: ptr(3)})
				return err
			}, `setting DNS quotas for project "` + prj + `": `},
		{"ResetQuota", expect{method: "DELETE", path: "/v2/quotas/" + prj}, 204, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return dns.ResetQuota(ctx, c, prj)
			}, `resetting DNS quotas for project "` + prj + `": `},
		{"ListTSIGKeys", expect{method: "GET", path: "/v2/tsigkeys?name=k"}, 200, `{"tsigkeys":[` + idJSON + `],"links":{}}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := dns.ListTSIGKeys(ctx, c, tsigkeys.ListOpts{Name: "k"})
				if err == nil && len(all) != 1 {
					t.Errorf("keys = %+v", all)
				}
				return err
			}, "listing TSIG keys: "},
		{"GetTSIGKey", expect{method: "GET", path: "/v2/tsigkeys/" + objID}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.GetTSIGKey(ctx, c, objID)
				return err
			}, `showing TSIG key "` + objID + `": `},
		{"CreateTSIGKey", expect{method: "POST", path: "/v2/tsigkeys",
			body: `{"name":"k","algorithm":"hmac-sha256","secret":"c2VjcmV0","scope":"POOL","resource_id":"` + objID + `"}`}, 201, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.CreateTSIGKey(ctx, c, tsigkeys.CreateOpts{
					Name: "k", Algorithm: "hmac-sha256", Secret: "c2VjcmV0", Scope: "POOL", ResourceID: objID,
				})
				return err
			}, `creating TSIG key "k": `},
		{"UpdateTSIGKey", expect{method: "PATCH", path: "/v2/tsigkeys/" + objID, body: `{"name":"k2"}`}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.UpdateTSIGKey(ctx, c, objID, tsigkeys.UpdateOpts{Name: "k2"})
				return err
			}, `updating TSIG key "` + objID + `": `},
		{"DeleteTSIGKey", expect{method: "DELETE", path: "/v2/tsigkeys/" + objID}, 204, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return dns.DeleteTSIGKey(ctx, c, objID)
			}, `deleting TSIG key "` + objID + `": `},
	}
}

func exportCases() []endpointCase {
	exports := "/v2/zones/tasks/exports"
	imports := "/v2/zones/tasks/imports"
	zonefile := "$ORIGIN example.com.\n"
	return []endpointCase{
		{"CreateZoneExport", expect{method: "POST", path: "/v2/zones/" + zoneID + "/tasks/export", body: noBody}, 202, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.CreateZoneExport(ctx, c, zoneID)
				return err
			}, "exporting zone " + zoneID + ": "},
		{"ListZoneExports", expect{method: "GET", path: exports + "?status=COMPLETE&zone_id=" + zoneID}, 200, `{"exports":[` + idJSON + `],"links":{}}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := dns.ListZoneExports(ctx, c, dns.ZoneExportListOpts{Status: "COMPLETE", ZoneID: zoneID}, 0)
				if err == nil && len(all) != 1 {
					t.Errorf("exports = %+v", all)
				}
				return err
			}, "listing zone exports: "},
		{"GetZoneExport", expect{method: "GET", path: exports + "/" + objID}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.GetZoneExport(ctx, c, objID)
				return err
			}, "showing zone export " + objID + ": "},
		{"GetZoneExportFile", expect{method: "GET", path: exports + "/" + objID + "/export", header: map[string]string{"Accept": "text/dns"}}, 200, zonefile,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				got, err := dns.GetZoneExportFile(ctx, c, objID)
				if err == nil && got != zonefile {
					t.Errorf("zonefile = %q", got)
				}
				return err
			}, "showing zonefile of export " + objID + ": "},
		{"DeleteZoneExport", expect{method: "DELETE", path: exports + "/" + objID}, 204, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return dns.DeleteZoneExport(ctx, c, objID)
			}, "deleting zone export " + objID + ": "},
		{"CreateZoneImport as text/dns", expect{method: "POST", path: imports, header: map[string]string{"Content-Type": "text/dns"}}, 202, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.CreateZoneImport(ctx, c, zonefile, nil)
				return err
			}, "importing zone: "},
		{"CreateZoneImport with attributes", expect{method: "POST", path: imports,
			body: `{"zonefile":"$ORIGIN example.com.\n","attributes":{"pool":"default"}}`}, 202, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.CreateZoneImport(ctx, c, zonefile, map[string]string{"pool": "default"})
				return err
			}, "importing zone: "},
		{"ListZoneImports", expect{method: "GET", path: imports + "?message=m&status=ERROR"}, 200, `{"imports":[` + idJSON + `],"links":{}}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := dns.ListZoneImports(ctx, c, dns.ZoneImportListOpts{Status: "ERROR", Message: "m"}, 0)
				if err == nil && len(all) != 1 {
					t.Errorf("imports = %+v", all)
				}
				return err
			}, "listing zone imports: "},
		{"GetZoneImport", expect{method: "GET", path: imports + "/" + objID}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.GetZoneImport(ctx, c, objID)
				return err
			}, "showing zone import " + objID + ": "},
		{"DeleteZoneImport", expect{method: "DELETE", path: imports + "/" + objID}, 204, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return dns.DeleteZoneImport(ctx, c, objID)
			}, "deleting zone import " + objID + ": "},
	}
}

func adminCases() []endpointCase {
	ptrs := "/v2/reverse/floatingips"
	return []endpointCase{
		{"GetLimits", expect{method: "GET", path: "/v2/limits"}, 200, `{"max_zones":10}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				l, err := dns.GetLimits(ctx, c)
				if err == nil && fmt.Sprint(l["max_zones"]) != "10" {
					t.Errorf("limits = %v", l)
				}
				return err
			}, "listing DNS limits: "},
		{"ListPools", expect{method: "GET", path: "/v2/pools?name=default"}, 200,
			`{"pools":[{"id":"` + objID + `","name":"default","ns_records":[{"hostname":"ns1.example.com.","priority":1}]}],"links":{}}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := dns.ListPools(ctx, c, "default", 0)
				if err == nil && (len(all) != 1 || len(all[0].NS) != 1) {
					t.Errorf("pools = %+v", all)
				}
				return err
			}, "listing DNS pools: "},
		{"GetPool", expect{method: "GET", path: "/v2/pools/" + objID}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.GetPool(ctx, c, objID)
				return err
			}, `showing DNS pool "` + objID + `": `},
		{"ListPTRRecords", expect{method: "GET", path: ptrs}, 200, `{"floatingips":[{"id":"` + ptrID + `","ttl":null}],"links":{}}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := dns.ListPTRRecords(ctx, c, 0)
				if err == nil && (len(all) != 1 || all[0].ID != ptrID || all[0].TTL != nil) {
					t.Errorf("ptr records = %+v", all)
				}
				return err
			}, "listing PTR records: "},
		{"GetPTRRecord", expect{method: "GET", path: ptrs + "/" + ptrID}, 200, `{"id":"` + ptrID + `"}`,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.GetPTRRecord(ctx, c, ptrID)
				return err
			}, "showing PTR record for floating IP " + ptrID + ": "},
		{"SetPTRRecord", expect{method: "PATCH", path: ptrs + "/" + ptrID, body: `{"ptrdname":"h.example.com.","ttl":null}`,
			header: map[string]string{"Content-Type": "application/json"}}, 202, `{"id":"` + ptrID + `"}`,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.SetPTRRecord(ctx, c, ptrID, map[string]any{"ptrdname": "h.example.com.", "ttl": nil})
				return err
			}, "setting PTR record for floating IP " + ptrID + ": "},
		{"UnsetPTRRecord", expect{method: "PATCH", path: ptrs + "/" + ptrID, body: `{"ptrdname":null}`}, 202, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return dns.UnsetPTRRecord(ctx, c, ptrID)
			}, "unsetting PTR record for floating IP " + ptrID + ": "},
		{"ListServiceStatuses", expect{method: "GET", path: "/v2/service_statuses?hostname=h&service_name=central&status=UP"}, 200,
			`{"service_statuses":[` + idJSON + `],"links":{}}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := dns.ListServiceStatuses(ctx, c, dns.ServiceStatusListOpts{Hostname: "h", ServiceName: "central", Status: "UP"}, 0)
				if err == nil && len(all) != 1 {
					t.Errorf("statuses = %+v", all)
				}
				return err
			}, "listing DNS service statuses: "},
		{"GetServiceStatus", expect{method: "GET", path: "/v2/service_statuses/" + objID}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.GetServiceStatus(ctx, c, objID)
				return err
			}, "showing DNS service status " + objID + ": "},
	}
}

func blacklistCases() []endpointCase {
	return []endpointCase{
		{"ListBlacklists", expect{method: "GET", path: "/v2/blacklists?pattern=bad"}, 200, `{"blacklists":[` + idJSON + `],"links":{}}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := dns.ListBlacklists(ctx, c, "bad", 0)
				if err == nil && len(all) != 1 {
					t.Errorf("blacklists = %+v", all)
				}
				return err
			}, "listing blacklists: "},
		{"GetBlacklist", expect{method: "GET", path: "/v2/blacklists/" + objID}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.GetBlacklist(ctx, c, objID)
				return err
			}, `showing blacklist "` + objID + `": `},
		{"CreateBlacklist", expect{method: "POST", path: "/v2/blacklists", body: `{"pattern":"bad"}`}, 201, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.CreateBlacklist(ctx, c, "bad", "")
				return err
			}, `creating blacklist "bad": `},
		{"CreateBlacklist with description", expect{method: "POST", path: "/v2/blacklists", body: `{"pattern":"bad","description":"d"}`}, 201, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.CreateBlacklist(ctx, c, "bad", "d")
				return err
			}, `creating blacklist "bad": `},
		{"UpdateBlacklist", expect{method: "PATCH", path: "/v2/blacklists/" + objID, body: `{"description":null}`}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.UpdateBlacklist(ctx, c, objID, map[string]any{"description": nil})
				return err
			}, `updating blacklist "` + objID + `": `},
		{"DeleteBlacklist", expect{method: "DELETE", path: "/v2/blacklists/" + objID}, 204, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return dns.DeleteBlacklist(ctx, c, objID)
			}, `deleting blacklist "` + objID + `": `},
		{"ListTLDs", expect{method: "GET", path: "/v2/tlds?name=com"}, 200, `{"tlds":[` + idJSON + `],"links":{}}`,
			func(t *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				all, err := dns.ListTLDs(ctx, c, "com", 0)
				if err == nil && len(all) != 1 {
					t.Errorf("tlds = %+v", all)
				}
				return err
			}, "listing TLDs: "},
		{"GetTLD", expect{method: "GET", path: "/v2/tlds/" + objID}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.GetTLD(ctx, c, objID)
				return err
			}, `showing TLD "` + objID + `": `},
		{"CreateTLD", expect{method: "POST", path: "/v2/tlds", body: `{"name":"com"}`}, 201, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.CreateTLD(ctx, c, "com", "")
				return err
			}, `creating TLD "com": `},
		{"CreateTLD with description", expect{method: "POST", path: "/v2/tlds", body: `{"name":"com","description":"d"}`}, 201, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.CreateTLD(ctx, c, "com", "d")
				return err
			}, `creating TLD "com": `},
		{"UpdateTLD", expect{method: "PATCH", path: "/v2/tlds/" + objID, body: `{"name":"org"}`}, 200, idJSON,
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.UpdateTLD(ctx, c, objID, map[string]any{"name": "org"})
				return err
			}, `updating TLD "` + objID + `": `},
		{"DeleteTLD", expect{method: "DELETE", path: "/v2/tlds/" + objID}, 204, "",
			func(_ *testing.T, ctx context.Context, c *gophercloud.ServiceClient) error {
				return dns.DeleteTLD(ctx, c, objID)
			}, `deleting TLD "` + objID + `": `},
	}
}

// TestEndpoints sends each call once to an endpoint that accepts it and once
// to one that answers 500, whose error must carry the call's text and the
// gophercloud error underneath.
func TestEndpoints(t *testing.T) {
	var all []endpointCase
	for _, group := range [][]endpointCase{
		zoneCases(), recordSetCases(), transferCases(), quotaCases(),
		exportCases(), adminCases(), blacklistCases(),
	} {
		all = append(all, group...)
	}
	for _, tc := range all {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := endpoint(t, tc.want, tc.status, tc.resp)
			if err := tc.call(t, context.Background(), c); err != nil {
				t.Fatalf("error = %v", err)
			}
			if *calls != 1 {
				t.Errorf("sent %d requests; want 1", *calls)
			}
		})
		t.Run(tc.name+" fails", func(t *testing.T) {
			c, _ := endpoint(t, tc.want, http.StatusInternalServerError, `{}`)
			err := tc.call(t, context.Background(), c)
			if err == nil || !strings.HasPrefix(err.Error(), tc.errPfx) {
				t.Fatalf("error = %v; want prefix %q", err, tc.errPfx)
			}
			if !gophercloud.ResponseCodeIs(err, http.StatusInternalServerError) {
				t.Errorf("error %v does not wrap the 500", err)
			}
		})
	}
}

// A page that does not decode fails the listing with what was being parsed.
func TestListDecodeErrors(t *testing.T) {
	tests := []struct {
		path, key, want string
		call            func(context.Context, *gophercloud.ServiceClient) error
	}{
		{"/v2/zones/tasks/exports", "exports", "listing zone exports: parsing zone export list: ",
			func(ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.ListZoneExports(ctx, c, dns.ZoneExportListOpts{}, 0)
				return err
			}},
		{"/v2/zones/tasks/imports", "imports", "listing zone imports: parsing zone import list: ",
			func(ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.ListZoneImports(ctx, c, dns.ZoneImportListOpts{}, 0)
				return err
			}},
		{"/v2/pools", "pools", "listing DNS pools: parsing pool list: ",
			func(ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.ListPools(ctx, c, "", 0)
				return err
			}},
		{"/v2/reverse/floatingips", "floatingips", "listing PTR records: parsing PTR record list: ",
			func(ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.ListPTRRecords(ctx, c, 0)
				return err
			}},
		{"/v2/service_statuses", "service_statuses", "listing DNS service statuses: parsing service status list: ",
			func(ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.ListServiceStatuses(ctx, c, dns.ServiceStatusListOpts{}, 0)
				return err
			}},
		{"/v2/blacklists", "blacklists", "listing blacklists: parsing blacklist list: ",
			func(ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.ListBlacklists(ctx, c, "", 0)
				return err
			}},
		{"/v2/tlds", "tlds", "listing TLDs: parsing TLD list: ",
			func(ctx context.Context, c *gophercloud.ServiceClient) error {
				_, err := dns.ListTLDs(ctx, c, "", 0)
				return err
			}},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			c, _ := endpoint(t, expect{method: "GET", path: tt.path}, 200, `{"`+tt.key+`":"not a list"}`)
			err := tt.call(context.Background(), c)
			var typeErr *json.UnmarshalTypeError
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) || !errors.As(err, &typeErr) {
				t.Errorf("error = %v; want prefix %q over a JSON type error", err, tt.want)
			}
		})
	}
}
