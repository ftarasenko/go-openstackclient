package network

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Tests for the dynamic-routing ("bgp speaker|peer|dragent") and TaaS mirror
// ("tap mirror") nouns. Every reference is a UUID, so no name lookup runs
// unless a test registers one.

const (
	bgptapSpeaker = "5e0b2c8e-0000-4000-8000-000000000001"
	bgptapPeer    = "5e0b2c8e-0000-4000-8000-000000000002"
	bgptapNet     = "5e0b2c8e-0000-4000-8000-000000000003"
	bgptapPort    = "5e0b2c8e-0000-4000-8000-000000000004"
	bgptapAgent   = "5e0b2c8e-0000-4000-8000-000000000005"
	bgptapMirror  = "5e0b2c8e-0000-4000-8000-000000000006"
	bgptapProject = "5e0b2c8e-0000-4000-8000-000000000007"
)

const bgptapSpeakerBody = `{"bgp_speaker":{"id":"` + bgptapSpeaker + `","name":"spk","local_as":65001,
  "ip_version":6,"advertise_floating_ip_host_routes":false,"advertise_tenant_networks":true,
  "networks":["` + bgptapNet + `"],"peers":["` + bgptapPeer + `"],"project_id":"` + bgptapProject + `"}}`

func bgptapSingle(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buf.String())
	}
	return got
}

func bgptapHeader(buf *bytes.Buffer) string {
	return strings.SplitN(buf.String(), "\n", 2)[0]
}

// --- bgp speaker ---------------------------------------------------------------

func TestRunBGPSpeakerCreate_SendsGivenAttributesOnly(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgp-speakers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"bgp_speaker":{"name":"spk","local_as":65001,"ip_version":6,
		  "advertise_tenant_networks":true,"advertise_floating_ip_host_routes":false,
		  "project_id":"`+bgptapProject+`"}}`)
		writeJSON(t, w, http.StatusCreated, bgptapSpeakerBody)
	})
	f := &bgpSpeakerCreateFlags{
		localAS: "65001", ipVersion: 6, projectID: bgptapProject,
		bgpSpeakerAdvertiseFlags: bgpSpeakerAdvertiseFlags{advertiseTenant: true, noAdvertiseFIP: true},
	}
	var buf bytes.Buffer
	if err := runBGPSpeakerCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, "spk", f, &buf); err != nil {
		t.Fatalf("runBGPSpeakerCreate: %v", err)
	}
	got := bgptapSingle(t, &buf)
	for k, want := range map[string]any{"id": bgptapSpeaker, "local_as": 65001.0, "ip_version": 6.0, "project_id": bgptapProject} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	if !reflect.DeepEqual(got["peers"], []any{bgptapPeer}) || !reflect.DeepEqual(got["networks"], []any{bgptapNet}) {
		t.Errorf("peers/networks = %v / %v", got["peers"], got["networks"])
	}
}

func TestRunBGPSpeakerCreate_DefaultsAndValidation(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgp-speakers", func(w http.ResponseWriter, r *http.Request) {
		// No advertise_* key unless a flag gave it: neutron's defaults apply.
		th.TestJSONRequest(t, r, `{"bgp_speaker":{"name":"spk","local_as":4294967295,"ip_version":4}}`)
		writeJSON(t, w, http.StatusCreated, bgptapSpeakerBody)
	})
	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runBGPSpeakerCreate(context.Background(), client, o, "spk", &bgpSpeakerCreateFlags{localAS: "4294967295", ipVersion: 4}, &buf); err != nil {
		t.Fatalf("runBGPSpeakerCreate: %v", err)
	}
	for _, f := range []*bgpSpeakerCreateFlags{
		{localAS: "0", ipVersion: 4},
		{localAS: "4294967296", ipVersion: 4},
		{localAS: "sixty", ipVersion: 4},
		{localAS: "65001", ipVersion: 5},
	} {
		if err := runBGPSpeakerCreate(context.Background(), client, o, "spk", f, &buf); err == nil {
			t.Errorf("create with %+v: expected a validation error", *f)
		}
	}
}

func TestRunBGPSpeakerList_AllAndByAgent(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	row := `{"id":"` + bgptapSpeaker + `","name":"spk","local_as":65001,"ip_version":4}`
	fakeServer.Mux.HandleFunc("/bgp-speakers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"bgp_speakers":[`+row+`]}`)
	})
	fakeServer.Mux.HandleFunc("/agents/"+bgptapAgent+"/bgp-drinstances", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"bgp_speakers":[`+row+`]}`)
	})
	o := &output.Options{Format: output.FormatCSV}
	for _, agent := range []string{"", bgptapAgent} {
		var buf bytes.Buffer
		if err := runBGPSpeakerList(context.Background(), networkClient(fakeServer), o, agent, &buf); err != nil {
			t.Fatalf("runBGPSpeakerList(agent=%q): %v", agent, err)
		}
		if h := bgptapHeader(&buf); h != "ID,Name,Local AS,IP Version" {
			t.Errorf("header = %s", h)
		}
		if !strings.Contains(buf.String(), bgptapSpeaker+",spk,65001,4") {
			t.Errorf("row missing:\n%s", buf.String())
		}
	}
}

func TestRunBGPSpeakerSet_SendsNameAndAdvertiseFlags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgp-speakers/"+bgptapSpeaker, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"bgp_speaker":{"name":"","advertise_tenant_networks":false,"advertise_floating_ip_host_routes":true}}`)
		writeJSON(t, w, http.StatusOK, bgptapSpeakerBody)
	})
	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatJSON}
	f := &bgpSpeakerSetFlags{bgpSpeakerAdvertiseFlags: bgpSpeakerAdvertiseFlags{noAdvertiseTenant: true, advertiseFIP: true}}
	var buf bytes.Buffer
	if err := runBGPSpeakerSet(context.Background(), client, o, bgptapSpeaker, f, fakeFlags{bgpFlagName: true}, &buf); err != nil {
		t.Fatalf("runBGPSpeakerSet: %v", err)
	}
	if err := runBGPSpeakerSet(context.Background(), client, o, bgptapSpeaker, &bgpSpeakerSetFlags{}, fakeFlags{}, &buf); err == nil {
		t.Error("set with no attribute flag: expected an error")
	}
}

// A name goes through ?name= and must match exactly; the delete then acts on
// the resolved ID, and every ref of a batch is attempted.
func TestRunBGPSpeakerShowAndDelete_ResolveByName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgp-speakers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		if got := r.URL.Query().Get("name"); got != "spk" {
			t.Errorf("lookup name = %q", got)
		}
		writeJSON(t, w, http.StatusOK, `{"bgp_speakers":[{"id":"`+bgptapSpeaker+`","name":"spk"},{"id":"other","name":"spk2"}]}`)
	})
	var deleted []string
	fakeServer.Mux.HandleFunc("/bgp-speakers/"+bgptapSpeaker, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(t, w, http.StatusOK, bgptapSpeakerBody)
		case http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s", r.Method)
		}
	})
	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runBGPSpeakerShow(context.Background(), client, &output.Options{Format: output.FormatJSON}, "spk", &buf); err != nil {
		t.Fatalf("runBGPSpeakerShow: %v", err)
	}
	if got := bgptapSingle(t, &buf); got["name"] != "spk" || got["advertise_tenant_networks"] != true {
		t.Errorf("show = %v", got)
	}
	buf.Reset()
	if err := runBGPSpeakerDelete(context.Background(), client, []string{"spk", bgptapSpeaker}, &buf); err != nil {
		t.Fatalf("runBGPSpeakerDelete: %v", err)
	}
	if len(deleted) != 2 || !strings.Contains(buf.String(), "Deleted BGP speaker spk") {
		t.Errorf("deleted %v, output %q", deleted, buf.String())
	}
}

// A name nothing matches errors (the strict resolver), and a server that
// ignores ?name= cannot make a near-miss match.
func TestResolveBGPSpeakerID_ZeroMatchErrors(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgp-speakers", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"bgp_speakers":[{"id":"`+bgptapSpeaker+`","name":"spk"}]}`)
	})
	_, err := resolveBGPSpeakerID(context.Background(), networkClient(fakeServer), "sp")
	if err == nil || !strings.Contains(err.Error(), `no BGP speaker found for "sp"`) {
		t.Errorf("error = %v", err)
	}
}

func TestRunBGPSpeakerAddRemoveNetworkAndPeer(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	for _, c := range []struct{ action, body string }{
		{"add_gateway_network", `{"network_id":"` + bgptapNet + `"}`},
		{"remove_gateway_network", `{"network_id":"` + bgptapNet + `"}`},
		{"add_bgp_peer", `{"bgp_peer_id":"` + bgptapPeer + `"}`},
		{"remove_bgp_peer", `{"bgp_peer_id":"` + bgptapPeer + `"}`},
	} {
		fakeServer.Mux.HandleFunc("/bgp-speakers/"+bgptapSpeaker+"/"+c.action, func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodPut)
			th.TestJSONRequest(t, r, c.body)
			writeJSON(t, w, http.StatusOK, c.body)
		})
	}
	client := networkClient(fakeServer)
	for _, c := range []struct {
		run  func(context.Context, *gophercloud.ServiceClient, string, string, io.Writer) error
		ref  string
		want string
	}{
		{runBGPSpeakerAddNetwork, bgptapNet, "Added network " + bgptapNet},
		{runBGPSpeakerRemoveNetwork, bgptapNet, "Removed network " + bgptapNet},
		{runBGPSpeakerAddPeer, bgptapPeer, "Added BGP peer " + bgptapPeer},
		{runBGPSpeakerRemovePeer, bgptapPeer, "Removed BGP peer " + bgptapPeer},
	} {
		var buf bytes.Buffer
		if err := c.run(context.Background(), client, bgptapSpeaker, c.ref, &buf); err != nil {
			t.Fatalf("%s: %v", c.want, err)
		}
		if !strings.Contains(buf.String(), c.want) {
			t.Errorf("output %q, want %q", buf.String(), c.want)
		}
	}
}

func TestRunBGPSpeakerListAdvertisedRoutes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgp-speakers/"+bgptapSpeaker+"/get_advertised_routes", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"advertised_routes":[{"destination":"192.0.2.0/24","next_hop":"198.51.100.1"}]}`)
	})
	var buf bytes.Buffer
	if err := runBGPSpeakerListAdvertisedRoutes(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatCSV}, bgptapSpeaker, &buf); err != nil {
		t.Fatalf("runBGPSpeakerListAdvertisedRoutes: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "Destination,Nexthop\n192.0.2.0/24,198.51.100.1" {
		t.Errorf("output =\n%s", got)
	}
}

// A cloud without neutron-dynamic-routing answers 404 on /bgp-speakers; the
// error names the missing extension.
func TestRunBGPSpeakerList_ExplainsAnUndeployedService(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"extensions":[{"alias":"router"}]}`)
	})
	fakeServer.Mux.HandleFunc("/bgp-speakers", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, `{"NeutronError":{"message":"not found"}}`)
	})
	err := runBGPSpeakerList(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatCSV}, "", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does not enable the bgp extension") {
		t.Errorf("error = %v", err)
	}
}

// --- bgp peer ------------------------------------------------------------------

const bgptapPeerBody = `{"bgp_peer":{"id":"` + bgptapPeer + `","name":"pr","peer_ip":"192.0.2.9",
  "remote_as":65002,"auth_type":"md5","project_id":"` + bgptapProject + `"}}`

func TestRunBGPPeerCreate_BodyAndAuthChecks(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgp-peers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"bgp_peer":{"name":"pr","peer_ip":"192.0.2.9","remote_as":65002,
		  "auth_type":"md5","password":"s3cret","project_id":"`+bgptapProject+`"}}`)
		writeJSON(t, w, http.StatusCreated, bgptapPeerBody)
	})
	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatJSON}
	f := &bgpPeerCreateFlags{peerIP: "192.0.2.9", remoteAS: "65002", authType: "MD5", password: "s3cret", projectID: bgptapProject}
	var buf bytes.Buffer
	if err := runBGPPeerCreate(context.Background(), client, o, "pr", f, fakeFlags{bgpFlagPassword: true}, &buf); err != nil {
		t.Fatalf("runBGPPeerCreate: %v", err)
	}
	got := bgptapSingle(t, &buf)
	if got["remote_as"] != 65002.0 || got["auth_type"] != "md5" || got["peer_ip"] != "192.0.2.9" {
		t.Errorf("show = %v", got)
	}
	for name, c := range map[string]struct {
		f     bgpPeerCreateFlags
		flags fakeFlags
	}{
		"md5 without password": {bgpPeerCreateFlags{peerIP: "192.0.2.9", remoteAS: "65002", authType: "md5"}, fakeFlags{}},
		"password without md5": {bgpPeerCreateFlags{peerIP: "192.0.2.9", remoteAS: "65002", authType: "none", password: "x"}, fakeFlags{bgpFlagPassword: true}},
		"unknown auth type":    {bgpPeerCreateFlags{peerIP: "192.0.2.9", remoteAS: "65002", authType: "sha1"}, fakeFlags{}},
		"bad remote AS":        {bgpPeerCreateFlags{peerIP: "192.0.2.9", remoteAS: "-1", authType: "none"}, fakeFlags{}},
	} {
		if err := runBGPPeerCreate(context.Background(), client, o, "pr", &c.f, c.flags, &buf); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestRunBGPPeerCreate_NoneOmitsPassword(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgp-peers", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"bgp_peer":{"name":"pr","peer_ip":"192.0.2.9","remote_as":65002,"auth_type":"none"}}`)
		writeJSON(t, w, http.StatusCreated, bgptapPeerBody)
	})
	f := &bgpPeerCreateFlags{peerIP: "192.0.2.9", remoteAS: "65002", authType: "none"}
	if err := runBGPPeerCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, "pr", f, fakeFlags{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runBGPPeerCreate: %v", err)
	}
}

func TestRunBGPPeerListSetShowDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/bgp-peers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"bgp_peers":[{"id":"`+bgptapPeer+`","name":"pr","peer_ip":"192.0.2.9","remote_as":65002}]}`)
	})
	fakeServer.Mux.HandleFunc("/bgp-peers/"+bgptapPeer, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			// Only the password: upstream's null password on a rename is not sent.
			th.TestJSONRequest(t, r, `{"bgp_peer":{"password":"n3w"}}`)
			writeJSON(t, w, http.StatusOK, bgptapPeerBody)
		case http.MethodGet:
			writeJSON(t, w, http.StatusOK, bgptapPeerBody)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runBGPPeerList(context.Background(), client, &output.Options{Format: output.FormatCSV}, &buf); err != nil {
		t.Fatalf("runBGPPeerList: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "ID,Name,Peer IP,Remote AS\n"+bgptapPeer+",pr,192.0.2.9,65002" {
		t.Errorf("list =\n%s", got)
	}
	o := &output.Options{Format: output.FormatJSON}
	buf.Reset()
	if err := runBGPPeerSet(context.Background(), client, o, bgptapPeer, &bgpPeerSetFlags{password: "n3w"}, fakeFlags{bgpFlagPassword: true}, &buf); err != nil {
		t.Fatalf("runBGPPeerSet: %v", err)
	}
	if err := runBGPPeerSet(context.Background(), client, o, bgptapPeer, &bgpPeerSetFlags{}, fakeFlags{}, &buf); err == nil {
		t.Error("set with no attribute flag: expected an error")
	}
	buf.Reset()
	if err := runBGPPeerShow(context.Background(), client, o, bgptapPeer, &buf); err != nil {
		t.Fatalf("runBGPPeerShow: %v", err)
	}
	if got := bgptapSingle(t, &buf); got["id"] != bgptapPeer || got["project_id"] != bgptapProject {
		t.Errorf("show = %v", got)
	}
	buf.Reset()
	if err := runBGPPeerDelete(context.Background(), client, []string{bgptapPeer}, &buf); err != nil {
		t.Fatalf("runBGPPeerDelete: %v", err)
	}
	if !strings.Contains(buf.String(), "Deleted BGP peer "+bgptapPeer) {
		t.Errorf("delete output %q", buf.String())
	}
}

// --- bgp dragent ---------------------------------------------------------------

const bgptapAgentRow = `{"id":"` + bgptapAgent + `","agent_type":"BGP dynamic routing agent","host":"dr1",
  "availability_zone":"nova","alive":true,"admin_state_up":true,"binary":"neutron-bgp-dragent"}`

func TestRunBGPDRAgentAddRemoveSpeaker(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/agents/"+bgptapAgent+"/bgp-drinstances", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"bgp_speaker_id":"`+bgptapSpeaker+`"}`)
		w.WriteHeader(http.StatusCreated)
	})
	fakeServer.Mux.HandleFunc("/agents/"+bgptapAgent+"/bgp-drinstances/"+bgptapSpeaker, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusNoContent)
	})
	client := networkClient(fakeServer)
	var buf bytes.Buffer
	if err := runBGPDRAgentAddSpeaker(context.Background(), client, bgptapAgent, bgptapSpeaker, &buf); err != nil {
		t.Fatalf("runBGPDRAgentAddSpeaker: %v", err)
	}
	if err := runBGPDRAgentRemoveSpeaker(context.Background(), client, bgptapAgent, bgptapSpeaker, &buf); err != nil {
		t.Fatalf("runBGPDRAgentRemoveSpeaker: %v", err)
	}
	if !strings.Contains(buf.String(), "Added BGP speaker") || !strings.Contains(buf.String(), "Removed BGP speaker") {
		t.Errorf("output %q", buf.String())
	}
}

func TestRunBGPDRAgentList_AllAndHostingSpeaker(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/agents", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		if got := r.URL.Query().Get("agent_type"); got != "BGP dynamic routing agent" {
			t.Errorf("agent_type = %q", got)
		}
		writeJSON(t, w, http.StatusOK, `{"agents":[`+bgptapAgentRow+`]}`)
	})
	fakeServer.Mux.HandleFunc("/bgp-speakers/"+bgptapSpeaker+"/bgp-dragents", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"agents":[`+bgptapAgentRow+`]}`)
	})
	o := &output.Options{Format: output.FormatCSV}
	for _, speaker := range []string{"", bgptapSpeaker} {
		var buf bytes.Buffer
		if err := runBGPDRAgentList(context.Background(), networkClient(fakeServer), o, speaker, &buf); err != nil {
			t.Fatalf("runBGPDRAgentList(%q): %v", speaker, err)
		}
		want := "ID,Agent Type,Host,Availability Zone,Alive,State,Binary\n" +
			bgptapAgent + ",BGP dynamic routing agent,dr1,nova,:-),UP,neutron-bgp-dragent"
		if got := strings.TrimSpace(buf.String()); got != want {
			t.Errorf("list(%q) =\n%s\nwant\n%s", speaker, got, want)
		}
	}
}

// --- tap mirror ----------------------------------------------------------------

const bgptapMirrorBody = `{"tap_mirror":{"id":"` + bgptapMirror + `","name":"m1","description":"d",
  "tenant_id":"` + bgptapProject + `","project_id":"` + bgptapProject + `","port_id":"` + bgptapPort + `",
  "directions":{"IN":101,"OUT":"102"},"remote_ip":"192.0.2.50","mirror_type":"erspanv1"}}`

func TestParseTapMirrorDirections(t *testing.T) {
	got, err := parseTapMirrorDirections([]string{`{"IN": 101}`, "OUT=102"})
	if err != nil {
		t.Fatalf("parseTapMirrorDirections: %v", err)
	}
	if want := map[string]any{"IN": 101.0, "OUT": "102"}; !reflect.DeepEqual(got, want) {
		t.Errorf("directions = %v, want %v", got, want)
	}
	if _, err := parseTapMirrorDirections([]string{"IN"}); err == nil {
		t.Error("a value that is neither JSON nor key=value: expected an error")
	}
}

func TestRunTapMirrorCreate_Body(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/taas/tap_mirrors", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"tap_mirror":{"name":"m1","description":"d","port_id":"`+bgptapPort+`",
		  "directions":{"IN":101,"OUT":"102"},"remote_ip":"192.0.2.50","mirror_type":"erspanv1",
		  "project_id":"`+bgptapProject+`"}}`)
		writeJSON(t, w, http.StatusCreated, bgptapMirrorBody)
	})
	f := &tapMirrorCreateFlags{
		name: "m1", description: "d", port: bgptapPort, directions: []string{`{"IN":101}`, "OUT=102"},
		remoteIP: "192.0.2.50", mirrorType: "erspanv1", projectID: bgptapProject,
	}
	var buf bytes.Buffer
	flags := fakeFlags{"name": true, flagDescription: true}
	if err := runTapMirrorCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, f, flags, &buf); err != nil {
		t.Fatalf("runTapMirrorCreate: %v", err)
	}
	got := bgptapSingle(t, &buf)
	if got["id"] != bgptapMirror || got["mirror_type"] != "erspanv1" || got["port_id"] != bgptapPort {
		t.Errorf("show = %v", got)
	}
	if !reflect.DeepEqual(got["directions"], map[string]any{"IN": 101.0, "OUT": "102"}) {
		t.Errorf("directions = %v", got["directions"])
	}
}

func TestRunTapMirrorList_ProjectFilterAndColumns(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/taas/tap_mirrors", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		if q := r.URL.Query(); len(q) != 1 || q.Get("project_id") != bgptapProject {
			t.Errorf("query = %v", q)
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(bgptapMirrorBody, `{"tap_mirror":`), "}")
		writeJSON(t, w, http.StatusOK, `{"tap_mirrors":[`+inner+`]}`)
	})
	var buf bytes.Buffer
	if err := runTapMirrorList(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatCSV}, bgptapProject, &buf); err != nil {
		t.Fatalf("runTapMirrorList: %v", err)
	}
	if h := bgptapHeader(&buf); h != "ID,Tenant,Name,Port,Directions,Remote IP,Mirror Type" {
		t.Errorf("header = %s", h)
	}
	for _, want := range []string{bgptapMirror, bgptapProject, bgptapPort, "192.0.2.50", "erspanv1", `""IN"":101`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q:\n%s", want, buf.String())
		}
	}
}

func TestRunTapMirrorShowUpdateDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var deletes int
	fakeServer.Mux.HandleFunc("/taas/tap_mirrors/"+bgptapMirror, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(t, w, http.StatusOK, bgptapMirrorBody)
		case http.MethodPut:
			th.TestJSONRequest(t, r, `{"tap_mirror":{"description":""}}`)
			writeJSON(t, w, http.StatusOK, bgptapMirrorBody)
		case http.MethodDelete:
			deletes++
			w.WriteHeader(http.StatusNoContent)
		}
	})
	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runTapMirrorShow(context.Background(), client, o, bgptapMirror, &buf); err != nil {
		t.Fatalf("runTapMirrorShow: %v", err)
	}
	if got := bgptapSingle(t, &buf); got["remote_ip"] != "192.0.2.50" || got["project_id"] != bgptapProject {
		t.Errorf("show = %v", got)
	}
	buf.Reset()
	if err := runTapMirrorUpdate(context.Background(), client, o, bgptapMirror, &tapMirrorUpdateFlags{}, fakeFlags{flagDescription: true}, &buf); err != nil {
		t.Fatalf("runTapMirrorUpdate: %v", err)
	}
	if err := runTapMirrorUpdate(context.Background(), client, o, bgptapMirror, &tapMirrorUpdateFlags{}, fakeFlags{}, &buf); err == nil {
		t.Error("update with no attribute flag: expected an error")
	}
	buf.Reset()
	if err := runTapMirrorDelete(context.Background(), client, []string{bgptapMirror, bgptapMirror}, &buf); err != nil {
		t.Fatalf("runTapMirrorDelete: %v", err)
	}
	if deletes != 2 || !strings.Contains(buf.String(), "Deleted tap mirror "+bgptapMirror) {
		t.Errorf("deletes = %d, output %q", deletes, buf.String())
	}
}

func TestRunTapMirrorShow_ExplainsAnUndeployedService(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"extensions":[{"alias":"router"}]}`)
	})
	fakeServer.Mux.HandleFunc("/taas/tap_mirrors/"+bgptapMirror, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, `{"NeutronError":{"message":"not found"}}`)
	})
	err := runTapMirrorShow(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatJSON}, bgptapMirror, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does not enable the tap-mirror extension") {
		t.Errorf("error = %v", err)
	}
}

// --- cobra: the nested upstream paths reach their seams --------------------------

func TestExec_BGPSpeakerCreate_FlagsReachTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/bgp-speakers", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"bgp_speaker":{"name":"spk","local_as":65001,"ip_version":4,"advertise_tenant_networks":false}}`)
		writeJSON(t, w, http.StatusCreated, bgptapSpeakerBody)
	})
	out, err := execNetwork(t, fakeServer, "bgp", "speaker", "create", "spk", "--local-as", "65001", "--no-advertise-tenant-networks")
	if err != nil {
		t.Fatalf("bgp speaker create: %v (%s)", err, out)
	}
	if _, err := execNetwork(t, fakeServer, "bgp", "speaker", "create", "spk", "--local-as", "1",
		"--advertise-tenant-networks", "--no-advertise-tenant-networks"); err == nil {
		t.Error("contradictory advertise flags: expected an error")
	}
}

func TestExec_BGPSpeakerListAdvertisedRoutes_NestedPath(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/bgp-speakers/"+bgptapSpeaker+"/get_advertised_routes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"advertised_routes":[{"destination":"192.0.2.0/24","next_hop":"198.51.100.1"}]}`)
	})
	out, err := execNetwork(t, fakeServer, "bgp", "speaker", "list", "advertised", "routes", bgptapSpeaker)
	if err != nil {
		t.Fatalf("bgp speaker list advertised routes: %v (%s)", err, out)
	}
	if !strings.Contains(out, "198.51.100.1") {
		t.Errorf("output:\n%s", out)
	}
}

func TestExec_BGPDRAgentAddSpeakerAndList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/agents/"+bgptapAgent+"/bgp-drinstances", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"bgp_speaker_id":"`+bgptapSpeaker+`"}`)
		w.WriteHeader(http.StatusCreated)
	})
	fakeServer.Mux.HandleFunc("/v2.0/bgp-speakers/"+bgptapSpeaker+"/bgp-dragents", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"agents":[`+bgptapAgentRow+`]}`)
	})
	if out, err := execNetwork(t, fakeServer, "bgp", "dragent", "add", "speaker", bgptapAgent, bgptapSpeaker); err != nil {
		t.Fatalf("bgp dragent add speaker: %v (%s)", err, out)
	}
	out, err := execNetwork(t, fakeServer, "bgp", "dragent", "list", "--bgp-speaker", bgptapSpeaker)
	if err != nil {
		t.Fatalf("bgp dragent list: %v (%s)", err, out)
	}
	if !strings.Contains(out, "neutron-bgp-dragent") {
		t.Errorf("output:\n%s", out)
	}
}

func TestExec_BGPPeerCreate_RequiredFlagsAndAuth(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/bgp-peers", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"bgp_peer":{"name":"pr","peer_ip":"192.0.2.9","remote_as":65002,"auth_type":"md5","password":"pw"}}`)
		writeJSON(t, w, http.StatusCreated, bgptapPeerBody)
	})
	if out, err := execNetwork(t, fakeServer, "bgp", "peer", "create", "pr", "--peer-ip", "192.0.2.9",
		"--remote-as", "65002", "--auth-type", "md5", "--password", "pw"); err != nil {
		t.Fatalf("bgp peer create: %v (%s)", err, out)
	}
	if _, err := execNetwork(t, fakeServer, "bgp", "peer", "create", "pr", "--peer-ip", "192.0.2.9"); err == nil {
		t.Error("missing --remote-as: expected an error")
	}
}

func TestExec_TapMirrorCreateAndUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/taas/tap_mirrors", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"tap_mirror":{"port_id":"`+bgptapPort+`","directions":{"IN":"5","OUT":"6"},
		  "remote_ip":"192.0.2.50","mirror_type":"gre"}}`)
		writeJSON(t, w, http.StatusCreated, bgptapMirrorBody)
	})
	fakeServer.Mux.HandleFunc("/v2.0/taas/tap_mirrors/"+bgptapMirror, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tap_mirror":{"name":"renamed"}}`)
		writeJSON(t, w, http.StatusOK, bgptapMirrorBody)
	})
	if out, err := execNetwork(t, fakeServer, "tap", "mirror", "create", "--port", bgptapPort,
		"--directions", "IN=5", "--directions", "OUT=6", "--remote-ip", "192.0.2.50", "--mirror-type", "gre"); err != nil {
		t.Fatalf("tap mirror create: %v (%s)", err, out)
	}
	if out, err := execNetwork(t, fakeServer, "tap", "mirror", "update", bgptapMirror, "--name", "renamed"); err != nil {
		t.Fatalf("tap mirror update: %v (%s)", err, out)
	}
}
