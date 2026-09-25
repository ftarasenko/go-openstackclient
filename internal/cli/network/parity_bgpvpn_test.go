package network

import (
	"bytes"
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/bgpvpns"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Synthetic IDs: every reference in these tests is a UUID so no resolver ever
// depends on a zero-match fallback.
const (
	vpnID     = "b0000000-0000-4000-8000-000000000001"
	vpnID2    = "b0000000-0000-4000-8000-000000000002"
	vpnID3    = "b0000000-0000-4000-8000-000000000003"
	vpnNetID  = "b1000000-0000-4000-8000-000000000001"
	vpnRtrID  = "b2000000-0000-4000-8000-000000000001"
	vpnPortID = "b3000000-0000-4000-8000-000000000001"
	vpnProjID = "b4000000-0000-4000-8000-000000000001"
	vpnAssoc  = "b5000000-0000-4000-8000-000000000001"
	vpnAssoc2 = "b5000000-0000-4000-8000-000000000002"
)

const bgpvpnBodyJSON = `{"bgpvpn":{"id":"` + vpnID + `","name":"vpn1","type":"l3",
  "route_targets":["64512:1"],"import_targets":["64512:2"],"export_targets":["64512:3"],
  "route_distinguishers":["64512:9"],"networks":["` + vpnNetID + `"],"routers":[],"ports":[],
  "local_pref":100,"vni":0,"project_id":"` + vpnProjID + `"}}`

func csvOut() *output.Options { return &output.Options{Format: output.FormatCSV} }

func TestRunBGPVPNList_FiltersAndColumns(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		want := map[string][]string{"project_id": {vpnProjID}, "type": {"l2"}, "vni": {"7"}}
		if q := map[string][]string(r.URL.Query()); !reflect.DeepEqual(q, want) {
			t.Errorf("query = %v, want %v", q, want)
		}
		writeJSON(t, w, http.StatusOK, `{"bgpvpns":[`+strings.TrimSuffix(strings.TrimPrefix(bgpvpnBodyJSON, `{"bgpvpn":`), "}")+`]}`)
	})
	client := networkClient(fakeServer)
	for _, tc := range []struct {
		long bool
		want string
	}{
		{false, "ID,Name,Type\n" + vpnID + ",vpn1,l3\n"},
		{true, "ID,Project,Name,Type,Route Targets,Import Targets,Export Targets,Route Distinguishers," +
			"Associated Networks,Associated Routers,Associated Ports,VNI,Local Pref\n" +
			vpnID + "," + vpnProjID + ",vpn1,l3,64512:1,64512:2,64512:3,64512:9," + vpnNetID + ",,,0,100\n"},
	} {
		var buf bytes.Buffer
		f := &bgpvpnListFlags{long: tc.long, projectID: vpnProjID, properties: []string{"type=l2", "vni=7"}}
		if err := runBGPVPNList(context.Background(), client, csvOut(), f, &buf); err != nil {
			t.Fatalf("runBGPVPNList: %v", err)
		}
		if buf.String() != tc.want {
			t.Errorf("long=%v output:\n%s\nwant:\n%s", tc.long, buf.String(), tc.want)
		}
	}
}

func TestRunBGPVPNCreate_SendsEveryAttribute(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"bgpvpn":{"name":"vpn1","type":"l2",
		  "route_targets":["64512:1","64512:4"],"import_targets":["64512:2"],"export_targets":["64512:3"],
		  "route_distinguishers":["64512:9"],"vni":0,"local_pref":100,"project_id":"`+vpnProjID+`"}}`)
		writeJSON(t, w, http.StatusCreated, bgpvpnBodyJSON)
	})
	f := &bgpvpnCreateFlags{
		name: "vpn1", bgpvpnType: "l2", routeTargets: []string{"64512:1", "64512:4"},
		importTargets: []string{"64512:2"}, exportTargets: []string{"64512:3"},
		routeDistinguishers: []string{"64512:9"}, vni: 0, localPref: 100, projectID: vpnProjID,
	}
	var buf bytes.Buffer
	flags := fakeFlags{"name": true, "vni": true, "local-pref": true}
	if err := runBGPVPNCreate(context.Background(), networkClient(fakeServer), csvOut(), f, flags, &buf); err != nil {
		t.Fatalf("runBGPVPNCreate: %v", err)
	}
	for _, want := range []string{"export_targets,64512:3", "local_pref,100", "networks," + vpnNetID, "type,l3"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, buf.String())
		}
	}
}

// Without flags upstream sends only the default type; a bad --type is refused
// before any request.
func TestRunBGPVPNCreate_DefaultsAndBadType(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"bgpvpn":{"type":"l3"}}`)
		writeJSON(t, w, http.StatusCreated, bgpvpnBodyJSON)
	})
	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runBGPVPNCreate(context.Background(), client, csvOut(), &bgpvpnCreateFlags{bgpvpnType: "l3"}, fakeFlags{}, &buf); err != nil {
		t.Fatalf("runBGPVPNCreate: %v", err)
	}
	if err := runBGPVPNCreate(context.Background(), client, csvOut(), &bgpvpnCreateFlags{bgpvpnType: "l4"}, fakeFlags{}, &buf); err == nil {
		t.Fatal("--type l4 accepted")
	}
}

func TestRunBGPVPNSet_MergesWithTheCurrentLists(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var methods []string
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns/"+vpnID, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodPut {
			th.TestJSONRequest(t, r, `{"bgpvpn":{"name":"vpn2","local_pref":50,
			  "route_targets":["64512:1","64512:5"],"import_targets":[],"route_distinguishers":["64512:9"]}}`)
		}
		writeJSON(t, w, http.StatusOK, bgpvpnBodyJSON)
	})
	f := &bgpvpnUpdateFlags{
		name: "vpn2", localPref: 50, routeTargets: []string{"64512:1", "64512:5"},
		purgeImportTargets: true, routeDistinguishers: []string{"64512:9"},
	}
	var buf bytes.Buffer
	if err := runBGPVPNUpdate(context.Background(), networkClient(fakeServer), csvOut(), vpnID, f, false,
		fakeFlags{"name": true, "local-pref": true}, &buf); err != nil {
		t.Fatalf("runBGPVPNUpdate: %v", err)
	}
	if !reflect.DeepEqual(methods, []string{http.MethodGet, http.MethodPut}) {
		t.Errorf("requests = %v, want GET then PUT", methods)
	}
	if !strings.Contains(buf.String(), "name,vpn1") {
		t.Errorf("output does not render the updated VPN:\n%s", buf.String())
	}
}

func TestRunBGPVPNUnset_RemovesAndPurges(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns/"+vpnID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			th.TestJSONRequest(t, r, `{"bgpvpn":{"vni":12,"route_targets":[],"export_targets":[],"route_distinguishers":[]}}`)
		}
		writeJSON(t, w, http.StatusOK, bgpvpnBodyJSON)
	})
	f := &bgpvpnUpdateFlags{
		vni: 12, routeTargets: []string{"64512:1"}, purgeExportTargets: true,
		routeDistinguishers: []string{"64512:9", "64512:77"},
	}
	var buf bytes.Buffer
	if err := runBGPVPNUpdate(context.Background(), networkClient(fakeServer), csvOut(), vpnID, f, true,
		fakeFlags{"vni": true}, &buf); err != nil {
		t.Fatalf("runBGPVPNUpdate(unset): %v", err)
	}
}

// Purging every list needs no read (upstream skips get_bgpvpn), and a set with
// nothing to send is refused.
func TestRunBGPVPNSet_PurgeAllSkipsTheReadAndEmptyIsRefused(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns/"+vpnID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"bgpvpn":{"route_targets":[],"import_targets":[],"export_targets":[],"route_distinguishers":[]}}`)
		writeJSON(t, w, http.StatusOK, bgpvpnBodyJSON)
	})
	client := networkClient(fakeServer)
	f := &bgpvpnUpdateFlags{
		routeTargets: []string{"x"}, purgeRouteTargets: true, purgeImportTargets: true,
		purgeExportTargets: true, purgeRDs: true,
	}
	var buf bytes.Buffer
	if err := runBGPVPNUpdate(context.Background(), client, csvOut(), vpnID, f, false, fakeFlags{}, &buf); err != nil {
		t.Fatalf("runBGPVPNUpdate: %v", err)
	}
	if err := runBGPVPNUpdate(context.Background(), client, csvOut(), vpnID, &bgpvpnUpdateFlags{}, false, fakeFlags{}, &buf); err == nil {
		t.Fatal("bgpvpn set with no flags succeeded")
	}
}

func TestRunBGPVPNShowAndDelete_ResolveByName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		if got := r.URL.Query().Get("name"); got != "vpn1" {
			t.Errorf("lookup name = %q, want vpn1", got)
		}
		writeJSON(t, w, http.StatusOK, `{"bgpvpns":[{"id":"`+vpnID+`","name":"vpn1"}]}`)
	})
	var deleted []string
	handle := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(t, w, http.StatusOK, bgpvpnBodyJSON)
	}
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns/"+vpnID, handle)
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns/"+vpnID2, handle)
	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runBGPVPNShow(context.Background(), client, csvOut(), "vpn1", &buf); err != nil {
		t.Fatalf("runBGPVPNShow: %v", err)
	}
	wantFields := []string{
		"export_targets", "id", "import_targets", "local_pref", "name", "networks", "ports",
		"project_id", "route_distinguishers", "route_targets", "routers", "type", "vni",
	}
	for _, field := range wantFields {
		if !strings.Contains(buf.String(), "\n"+field+",") {
			t.Errorf("show lacks field %s:\n%s", field, buf.String())
		}
	}
	if err := runBGPVPNDelete(context.Background(), client, []string{"vpn1", vpnID2}); err != nil {
		t.Fatalf("runBGPVPNDelete: %v", err)
	}
	if want := []string{"/bgpvpn/bgpvpns/" + vpnID, "/bgpvpn/bgpvpns/" + vpnID2}; !reflect.DeepEqual(deleted, want) {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
}

// A cloud without the plugin answers 404 on every bgpvpn path; the error names
// the extension. A port association on a cloud with bgpvpn but without
// bgpvpn-routes-control names that one instead.
func TestBGPVPN_ExplainsAnUndeployedPlugin(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	exts := `{"extensions":[{"alias":"router"}]}`
	fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, exts)
	})
	notFound := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, `{"NeutronError":{"message":"not found"}}`)
	}
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns", notFound)
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns/"+vpnID+"/port_associations", notFound)
	client := networkClient(fakeServer)
	var buf bytes.Buffer
	err := runBGPVPNList(context.Background(), client, csvOut(), &bgpvpnListFlags{}, &buf)
	if err == nil || !strings.Contains(err.Error(), "does not enable the bgpvpn extension") {
		t.Errorf("bgpvpn list error = %v, want the bgpvpn extension named", err)
	}
	err = runBGPVPNPortAssocList(context.Background(), client, csvOut(), vpnID, &bgpvpnAssocListFlags{}, &buf)
	if err == nil || !strings.Contains(err.Error(), "does not enable the bgpvpn extension") {
		t.Errorf("port association list error = %v, want the bgpvpn extension named", err)
	}
	exts = `{"extensions":[{"alias":"bgpvpn"}]}`
	err = runBGPVPNPortAssocList(context.Background(), client, csvOut(), vpnID, &bgpvpnAssocListFlags{}, &buf)
	if err == nil || !strings.Contains(err.Error(), "does not enable the bgpvpn-routes-control extension") {
		t.Errorf("port association list error = %v, want bgpvpn-routes-control named", err)
	}
}

// --- network association -----------------------------------------------------

const netAssocJSON = `{"id":"` + vpnAssoc + `","network_id":"` + vpnNetID + `","project_id":"` + vpnProjID + `"}`

func TestRunBGPVPNNetworkAssoc_Verbs(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	base := "/bgpvpn/bgpvpns/" + vpnID + "/network_associations"
	fakeServer.Mux.HandleFunc(base, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			th.TestJSONRequest(t, r, `{"network_association":{"network_id":"`+vpnNetID+`","project_id":"`+vpnProjID+`"}}`)
			writeJSON(t, w, http.StatusCreated, `{"network_association":`+netAssocJSON+`}`)
		default:
			if got := r.URL.Query().Get("network_id"); got != vpnNetID {
				t.Errorf("list filter network_id = %q", got)
			}
			writeJSON(t, w, http.StatusOK, `{"network_associations":[`+netAssocJSON+`]}`)
		}
	})
	var deleted []string
	assocHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(t, w, http.StatusOK, `{"network_association":`+netAssocJSON+`}`)
	}
	fakeServer.Mux.HandleFunc(base+"/"+vpnAssoc, assocHandler)
	fakeServer.Mux.HandleFunc(base+"/"+vpnAssoc2, assocHandler)
	client := networkClient(fakeServer)
	ctx := context.Background()

	var buf bytes.Buffer
	if err := runBGPVPNNetworkAssocCreate(ctx, client, csvOut(), vpnID, vpnNetID, vpnProjID, &buf); err != nil {
		t.Fatalf("create: %v", err)
	}
	if want := "Field,Value\nid," + vpnAssoc + "\nnetwork_id," + vpnNetID + "\nproject_id," + vpnProjID + "\n"; buf.String() != want {
		t.Errorf("create output:\n%s\nwant:\n%s", buf.String(), want)
	}
	for _, tc := range []struct {
		long bool
		want string
	}{
		{false, "ID,Network ID\n" + vpnAssoc + "," + vpnNetID + "\n"},
		{true, "ID,Project,Network ID\n" + vpnAssoc + "," + vpnProjID + "," + vpnNetID + "\n"},
	} {
		buf.Reset()
		f := &bgpvpnAssocListFlags{long: tc.long, properties: []string{"network_id=" + vpnNetID}}
		if err := runBGPVPNNetworkAssocList(ctx, client, csvOut(), vpnID, f, &buf); err != nil {
			t.Fatalf("list: %v", err)
		}
		if buf.String() != tc.want {
			t.Errorf("list long=%v:\n%s\nwant:\n%s", tc.long, buf.String(), tc.want)
		}
	}
	buf.Reset()
	if err := runBGPVPNNetworkAssocShow(ctx, client, csvOut(), vpnID, vpnAssoc, &buf); err != nil {
		t.Fatalf("show: %v", err)
	}
	del := func(ctx context.Context, c *gophercloud.ServiceClient, b, id string) error {
		return bgpvpns.DeleteNetworkAssociation(ctx, c, b, id).ExtractErr()
	}
	if err := runBGPVPNAssocDelete(ctx, client, bgpvpnNetworkAssoc, del, vpnID, []string{vpnAssoc, vpnAssoc2}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if want := []string{base + "/" + vpnAssoc, base + "/" + vpnAssoc2}; !reflect.DeepEqual(deleted, want) {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
}

// --- router association ------------------------------------------------------

const rtrAssocJSON = `{"id":"` + vpnAssoc + `","router_id":"` + vpnRtrID + `","project_id":"` + vpnProjID + `","advertise_extra_routes":true}`

func TestRunBGPVPNRouterAssoc_AdvertiseSemantics(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	base := "/bgpvpn/bgpvpns/" + vpnID + "/router_associations"
	var wantBody string
	fakeServer.Mux.HandleFunc(base, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			th.TestJSONRequest(t, r, wantBody)
			writeJSON(t, w, http.StatusCreated, `{"router_association":`+rtrAssocJSON+`}`)
			return
		}
		writeJSON(t, w, http.StatusOK, `{"router_associations":[`+rtrAssocJSON+`]}`)
	})
	fakeServer.Mux.HandleFunc(base+"/"+vpnAssoc, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			th.TestJSONRequest(t, r, wantBody)
		}
		writeJSON(t, w, http.StatusOK, `{"router_association":`+rtrAssocJSON+`}`)
	})
	client := networkClient(fakeServer)
	ctx := context.Background()
	var buf bytes.Buffer

	// Upstream always sends the attribute, false unless a flag says otherwise.
	wantBody = `{"router_association":{"router_id":"` + vpnRtrID + `","project_id":"` + vpnProjID + `","advertise_extra_routes":false}}`
	if err := runBGPVPNRouterAssocCreate(ctx, client, csvOut(), vpnID, vpnRtrID, vpnProjID, bgpvpnAdvertiseFlags{}, &buf); err != nil {
		t.Fatalf("create: %v", err)
	}
	wantBody = `{"router_association":{"router_id":"` + vpnRtrID + `","advertise_extra_routes":true}}`
	if err := runBGPVPNRouterAssocCreate(ctx, client, csvOut(), vpnID, vpnRtrID, "", bgpvpnAdvertiseFlags{advertise: true}, &buf); err != nil {
		t.Fatalf("create --advertise_extra_routes: %v", err)
	}
	for _, tc := range []struct {
		adv   bgpvpnAdvertiseFlags
		unset bool
		want  bool
	}{
		{bgpvpnAdvertiseFlags{advertise: true}, false, true},
		{bgpvpnAdvertiseFlags{noAdvertise: true}, false, false},
		{bgpvpnAdvertiseFlags{advertise: true}, true, false},
		{bgpvpnAdvertiseFlags{noAdvertise: true}, true, true},
		{bgpvpnAdvertiseFlags{}, false, false},
	} {
		wantBody = `{"router_association":{"advertise_extra_routes":` + map[bool]string{true: "true", false: "false"}[tc.want] + `}}`
		if err := runBGPVPNRouterAssocUpdate(ctx, client, csvOut(), vpnID, vpnAssoc, tc.adv, tc.unset, &buf); err != nil {
			t.Fatalf("update %+v unset=%v: %v", tc.adv, tc.unset, err)
		}
	}
	buf.Reset()
	if err := runBGPVPNRouterAssocList(ctx, client, csvOut(), vpnID, &bgpvpnAssocListFlags{long: true}, &buf); err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := "ID,Project,Router ID,Advertise extra routes\n" + vpnAssoc + "," + vpnProjID + "," + vpnRtrID + ",true\n"; buf.String() != want {
		t.Errorf("list --long:\n%s\nwant:\n%s", buf.String(), want)
	}
	buf.Reset()
	if err := runBGPVPNRouterAssocShow(ctx, client, csvOut(), vpnID, vpnAssoc, &buf); err != nil {
		t.Fatalf("show: %v", err)
	}
	if want := "Field,Value\nadvertise_extra_routes,true\nid," + vpnAssoc + "\nproject_id," + vpnProjID + "\nrouter_id," + vpnRtrID + "\n"; buf.String() != want {
		t.Errorf("show:\n%s\nwant:\n%s", buf.String(), want)
	}
}

// --- port association --------------------------------------------------------

const portAssocJSON = `{"id":"` + vpnAssoc + `","port_id":"` + vpnPortID + `","project_id":"` + vpnProjID + `",
  "advertise_fixed_ips":true,"routes":[
    {"type":"prefix","prefix":"192.0.2.0/24","local_pref":100},
    {"type":"prefix","prefix":"198.51.100.0/24"},
    {"type":"bgpvpn","bgpvpn_id":"` + vpnID2 + `","local_pref":50}]}`

func TestRunBGPVPNPortAssocCreate_BuildsRoutes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("name"); got != "leak" {
			t.Errorf("bgpvpn lookup name = %q, want leak", got)
		}
		writeJSON(t, w, http.StatusOK, `{"bgpvpns":[{"id":"`+vpnID2+`","name":"leak"}]}`)
	})
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns/"+vpnID+"/port_associations", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"port_association":{"port_id":"`+vpnPortID+`","project_id":"`+vpnProjID+`",
		  "advertise_fixed_ips":false,"routes":[
		    {"type":"prefix","prefix":"192.0.2.0/24","local_pref":100},
		    {"type":"prefix","prefix":"198.51.100.0/24"},
		    {"type":"bgpvpn","bgpvpn_id":"`+vpnID2+`","local_pref":50}]}}`)
		writeJSON(t, w, http.StatusCreated, `{"port_association":`+portAssocJSON+`}`)
	})
	f := &bgpvpnPortAssocFlags{
		adv:          bgpvpnAdvertiseFlags{noAdvertise: true},
		prefixRoutes: []string{"prefix=192.0.2.0/24,local_pref=10", "prefix=198.51.100.0/24", "prefix=192.0.2.0/24,local_pref=100"},
		bgpvpnRoutes: []string{"bgpvpn=leak,local_pref=50"},
	}
	var buf bytes.Buffer
	if err := runBGPVPNPortAssocCreate(context.Background(), networkClient(fakeServer), csvOut(),
		vpnID, vpnPortID, vpnProjID, f, &buf); err != nil {
		t.Fatalf("create: %v", err)
	}
	want := "Field,Value\nadvertise_fixed_ips,true\nbgpvpn_routes," + vpnID2 + " (50)\nid," + vpnAssoc +
		"\nport_id," + vpnPortID + "\nprefix_routes,\"192.0.2.0/24 (100), 198.51.100.0/24\"\nproject_id," + vpnProjID + "\n"
	if buf.String() != want {
		t.Errorf("create output:\n%s\nwant:\n%s", buf.String(), want)
	}
}

// Without route flags create still sends routes: [] (the typed opts would drop it).
func TestRunBGPVPNPortAssocCreate_EmptyRoutesAndBadSpecs(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns/"+vpnID+"/port_associations", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"port_association":{"port_id":"`+vpnPortID+`","routes":[]}}`)
		writeJSON(t, w, http.StatusCreated, `{"port_association":`+portAssocJSON+`}`)
	})
	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runBGPVPNPortAssocCreate(context.Background(), client, csvOut(), vpnID, vpnPortID, "", &bgpvpnPortAssocFlags{}, &buf); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, spec := range []string{"192.0.2.0/24", "prefix=192.0.2.0/24,local_pref=x", "prefix=192.0.2.0/24,weight=1", "local_pref=1"} {
		f := &bgpvpnPortAssocFlags{prefixRoutes: []string{spec}}
		if err := runBGPVPNPortAssocCreate(context.Background(), client, csvOut(), vpnID, vpnPortID, "", f, &buf); err == nil {
			t.Errorf("--prefix-route %q accepted", spec)
		}
	}
}

func TestRunBGPVPNPortAssocSetUnset_EditTheCurrentRoutes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var wantBody string
	var methods []string
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns/"+vpnID+"/port_associations/"+vpnAssoc, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodPut {
			th.TestJSONRequest(t, r, wantBody)
		}
		writeJSON(t, w, http.StatusOK, `{"port_association":`+portAssocJSON+`}`)
	})
	client := networkClient(fakeServer)
	ctx := context.Background()
	var buf bytes.Buffer

	// set: an existing prefix keeps its place with the new local_pref, a new
	// one is appended, and a new BGP VPN route joins the existing one.
	wantBody = `{"port_association":{"advertise_fixed_ips":true,"routes":[
	  {"type":"prefix","prefix":"192.0.2.0/24","local_pref":7},
	  {"type":"prefix","prefix":"198.51.100.0/24"},
	  {"type":"prefix","prefix":"203.0.113.0/24"},
	  {"type":"bgpvpn","bgpvpn_id":"` + vpnID2 + `","local_pref":50},
	  {"type":"bgpvpn","bgpvpn_id":"` + vpnID3 + `"}]}}`
	set := &bgpvpnPortAssocFlags{
		adv:          bgpvpnAdvertiseFlags{advertise: true},
		prefixRoutes: []string{"prefix=192.0.2.0/24,local_pref=7", "prefix=203.0.113.0/24"},
		bgpvpnRoutes: []string{"bgpvpn=" + vpnID3},
	}
	if err := runBGPVPNPortAssocUpdate(ctx, client, csvOut(), vpnID, vpnAssoc, set, false, &buf); err != nil {
		t.Fatalf("set: %v", err)
	}
	if !reflect.DeepEqual(methods, []string{http.MethodGet, http.MethodPut}) {
		t.Errorf("set requests = %v, want GET then PUT", methods)
	}

	// set --no-prefix-route --no-bgpvpn-route empties the routes.
	wantBody = `{"port_association":{"routes":[]}}`
	if err := runBGPVPNPortAssocUpdate(ctx, client, csvOut(), vpnID, vpnAssoc,
		&bgpvpnPortAssocFlags{purgePrefix: true, purgeBGPVPN: true}, false, &buf); err != nil {
		t.Fatalf("set --no-*: %v", err)
	}

	// unset: remove one prefix and the VPN route; --advertise-fixed-ips on
	// unset means "stop advertising".
	wantBody = `{"port_association":{"advertise_fixed_ips":false,"routes":[
	  {"type":"prefix","prefix":"192.0.2.0/24","local_pref":100}]}}`
	unset := &bgpvpnPortAssocFlags{
		adv:          bgpvpnAdvertiseFlags{advertise: true},
		prefixRoutes: []string{"198.51.100.0/24"},
		bgpvpnRoutes: []string{vpnID2},
	}
	if err := runBGPVPNPortAssocUpdate(ctx, client, csvOut(), vpnID, vpnAssoc, unset, true, &buf); err != nil {
		t.Fatalf("unset: %v", err)
	}
}

func TestRunBGPVPNPortAssocListAndShow_Columns(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgpvpn/bgpvpns/"+vpnID+"/port_associations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"port_associations":[`+portAssocJSON+`]}`)
	})
	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runBGPVPNPortAssocList(context.Background(), client, csvOut(), vpnID, &bgpvpnAssocListFlags{}, &buf); err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := "ID,Port ID\n" + vpnAssoc + "," + vpnPortID + "\n"; buf.String() != want {
		t.Errorf("list:\n%s\nwant:\n%s", buf.String(), want)
	}
	buf.Reset()
	if err := runBGPVPNPortAssocList(context.Background(), client, csvOut(), vpnID, &bgpvpnAssocListFlags{long: true}, &buf); err != nil {
		t.Fatalf("list --long: %v", err)
	}
	want := "ID,Project,Port ID,Prefix Routes (BGP LOCAL_PREF),BGP VPN Routes (BGP LOCAL_PREF),Advertise Port's Fixed IPs\n" +
		vpnAssoc + "," + vpnProjID + "," + vpnPortID + ",\"192.0.2.0/24 (100), 198.51.100.0/24\"," + vpnID2 + " (50),true\n"
	if buf.String() != want {
		t.Errorf("list --long:\n%s\nwant:\n%s", buf.String(), want)
	}
}

// --- cobra wiring ------------------------------------------------------------

// The nested paths ("bgpvpn network association create", …) resolve, and a
// representative flag of each verb reaches its request.
func TestExec_BGPVPN_NestedPathsAndFlags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var got []string
	record := func(want string, reply string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			got = append(got, r.Method+" "+r.URL.Path)
			if want != "" && r.Method != http.MethodGet {
				th.TestJSONRequest(t, r, want)
			}
			status := http.StatusOK
			if r.Method == http.MethodPost {
				status = http.StatusCreated
			}
			writeJSON(t, w, status, reply)
		}
	}
	v2 := "/v2.0/bgpvpn/bgpvpns"
	fakeServer.Mux.HandleFunc(v2, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if r.Method == http.MethodPost {
			th.TestJSONRequest(t, r, `{"bgpvpn":{"type":"l3","vni":0,"route_targets":["64512:1"]}}`)
			writeJSON(t, w, http.StatusCreated, bgpvpnBodyJSON)
			return
		}
		writeJSON(t, w, http.StatusOK, `{"bgpvpns":[]}`)
	})
	fakeServer.Mux.HandleFunc(v2+"/"+vpnID+"/network_associations",
		record(`{"network_association":{"network_id":"`+vpnNetID+`","project_id":"`+vpnProjID+`"}}`,
			`{"network_association":`+netAssocJSON+`}`))
	fakeServer.Mux.HandleFunc(v2+"/"+vpnID+"/router_associations/"+vpnAssoc,
		record(`{"router_association":{"advertise_extra_routes":true}}`, `{"router_association":`+rtrAssocJSON+`}`))
	fakeServer.Mux.HandleFunc(v2+"/"+vpnID+"/port_associations/"+vpnAssoc,
		record(`{"port_association":{"routes":[{"type":"bgpvpn","bgpvpn_id":"`+vpnID2+`","local_pref":50}]}}`,
			`{"port_association":`+portAssocJSON+`}`))

	for _, argv := range [][]string{
		{"bgpvpn", "create", "--vni", "0", "--route-target", "64512:1"},
		{"bgpvpn", "list", "--property", "type=l2"},
		{"bgpvpn", "network", "association", "create", vpnID, vpnNetID, "--project", vpnProjID},
		{"bgpvpn", "router", "association", "set", vpnAssoc, vpnID, "--advertise_extra_routes"},
		{"bgpvpn", "port", "association", "unset", vpnAssoc, vpnID, "--all-prefix-routes"},
	} {
		if out, err := execNetwork(t, fakeServer, argv...); err != nil {
			t.Fatalf("koc %s: %v\n%s", strings.Join(argv, " "), err, out)
		}
	}
	want := []string{
		"POST /v2.0/bgpvpn/bgpvpns?",
		"GET /v2.0/bgpvpn/bgpvpns?type=l2",
		"POST " + v2 + "/" + vpnID + "/network_associations",
		"PUT " + v2 + "/" + vpnID + "/router_associations/" + vpnAssoc,
		"GET " + v2 + "/" + vpnID + "/port_associations/" + vpnAssoc,
		"PUT " + v2 + "/" + vpnID + "/port_associations/" + vpnAssoc,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Both advertise flags at once are refused by cobra before any request.
	if _, err := execNetwork(t, fakeServer, "bgpvpn", "router", "association", "create", vpnID, vpnRtrID,
		"--advertise_extra_routes", "--no-advertise_extra_routes"); err == nil {
		t.Error("contradictory advertise flags accepted")
	}
}
