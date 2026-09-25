package network

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Synthetic IDs for the VPNaaS tests. IDs are UUIDs throughout so the VPN
// resolvers short-circuit and no test relies on a zero-match fallback.
const (
	vpnSvcID     = "11111111-0000-4000-8000-000000000001"
	vpnaasRtrID  = "11111111-0000-4000-8000-000000000002"
	vpnSubID     = "11111111-0000-4000-8000-000000000003"
	vpnFlvID     = "11111111-0000-4000-8000-000000000004"
	vpnIKEID     = "11111111-0000-4000-8000-000000000005"
	vpnIPsecID   = "11111111-0000-4000-8000-000000000006"
	vpnEPGLID    = "11111111-0000-4000-8000-000000000007"
	vpnEPGPID    = "11111111-0000-4000-8000-000000000008"
	vpnConnID    = "11111111-0000-4000-8000-000000000009"
	vpnaasProjID = "11111111-0000-4000-8000-00000000000a"
	vpnSub2ID    = "11111111-0000-4000-8000-00000000000b"
)

// oneMatch answers a name-filtered collection GET with exactly one resource
// whose ID is id, so a name resolves to a known UUID.
func oneMatch(t *testing.T, fakeServer th.FakeServer, path, key, id string) {
	t.Helper()
	fakeServer.Mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{%q:[{"id":%q,"name":%q}]}`, key, id, r.URL.Query().Get("name")))
	})
}

// vpnWrite registers a handler asserting method and exact JSON body, answering
// with resp.
func vpnWrite(t *testing.T, fakeServer th.FakeServer, path, method, wantBody, resp string, status int) *int {
	t.Helper()
	calls := 0
	fakeServer.Mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		calls++
		th.TestMethod(t, r, method)
		th.TestJSONRequest(t, r, wantBody)
		writeJSON(t, w, status, resp)
	})
	return &calls
}

func vpnaasCSVOut() *output.Options { return &output.Options{Format: output.FormatCSV} }

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

// ---- vpn service ----

func TestRunVPNServiceCreate_SendsUpstreamBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	oneMatch(t, fakeServer, "/subnets", "subnets", vpnSubID)
	oneMatch(t, fakeServer, "/routers", "routers", vpnaasRtrID)
	fakeServer.Mux.HandleFunc("/flavors", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("name"); got != "gold" {
			t.Errorf("flavor lookup name = %q, want gold", got)
		}
		writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{"flavors":[{"id":%q}]}`, vpnFlvID))
	})
	vpnWrite(t, fakeServer, "/vpn/vpnservices", http.MethodPost, fmt.Sprintf(`{"vpnservice":{
		"name":"vpn1","description":"d","subnet_id":%q,"flavor_id":%q,"admin_state_up":false,
		"router_id":%q,"project_id":%q}}`, vpnSubID, vpnFlvID, vpnaasRtrID, vpnaasProjID),
		fmt.Sprintf(`{"vpnservice":{"id":%q,"name":"vpn1","router_id":%q,"status":"PENDING_CREATE"}}`, vpnSvcID, vpnaasRtrID),
		http.StatusCreated)

	f := &vpnServiceFlags{name: "vpn1", description: "d", subnet: "priv", flavor: "gold", router: "r1", disable: true, projectID: vpnaasProjID}
	var buf bytes.Buffer
	if err := runVPNServiceCreate(context.Background(), networkClient(fakeServer), vpnaasCSVOut(), f, &buf); err != nil {
		t.Fatalf("runVPNServiceCreate: %v", err)
	}
	if want := "Field,Value"; firstLine(buf.String()) != want {
		t.Errorf("header = %q", firstLine(buf.String()))
	}
	for _, want := range []string{"Router," + vpnaasRtrID, "Status,PENDING_CREATE", "Ext v4 IP,", "Flavor,"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, buf.String())
		}
	}
}

// Without --enable/--disable, admin_state_up is not sent at all (gophercloud's
// CreateOpts would send null).
func TestRunVPNServiceCreate_MinimalBodyOmitsUnsetAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	oneMatch(t, fakeServer, "/routers", "routers", vpnaasRtrID)
	vpnWrite(t, fakeServer, "/vpn/vpnservices", http.MethodPost,
		fmt.Sprintf(`{"vpnservice":{"name":"vpn1","router_id":%q}}`, vpnaasRtrID),
		fmt.Sprintf(`{"vpnservice":{"id":%q}}`, vpnSvcID), http.StatusCreated)
	var buf bytes.Buffer
	if err := runVPNServiceCreate(context.Background(), networkClient(fakeServer), vpnaasCSVOut(),
		&vpnServiceFlags{name: "vpn1", router: vpnaasRtrID}, &buf); err != nil {
		t.Fatalf("runVPNServiceCreate: %v", err)
	}
}

func TestRunVPNServiceList_Columns(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/vpn/vpnservices", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want none", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{"vpnservices":[{"id":%q,"name":"vpn1","router_id":%q,
		  "subnet_id":%q,"flavor_id":%q,"admin_state_up":true,"status":"ACTIVE","description":"d",
		  "project_id":%q,"external_v4_ip":"203.0.113.9","external_v6_ip":"2001:db8::9"}]}`,
			vpnSvcID, vpnaasRtrID, vpnSubID, vpnFlvID, vpnaasProjID))
	})
	for _, tc := range []struct {
		long bool
		want string
	}{
		{false, "ID,Name,Router,Subnet,Flavor,State,Status\n" +
			strings.Join([]string{vpnSvcID, "vpn1", vpnaasRtrID, vpnSubID, vpnFlvID, "true", "ACTIVE"}, ",") + "\n"},
		{true, "ID,Name,Router,Subnet,Flavor,State,Status,Description,Project,Ext v4 IP,Ext v6 IP\n" +
			strings.Join([]string{vpnSvcID, "vpn1", vpnaasRtrID, vpnSubID, vpnFlvID, "true", "ACTIVE", "d", vpnaasProjID, "203.0.113.9", "2001:db8::9"}, ",") + "\n"},
	} {
		var buf bytes.Buffer
		if err := runVPNServiceList(context.Background(), networkClient(fakeServer), vpnaasCSVOut(), tc.long, &buf); err != nil {
			t.Fatalf("runVPNServiceList: %v", err)
		}
		if buf.String() != tc.want {
			t.Errorf("long=%v output =\n%s\nwant\n%s", tc.long, buf.String(), tc.want)
		}
	}
}

func TestRunVPNServiceShowSetDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	// A name resolves through a name-filtered list; the ID then drives the rest.
	fakeServer.Mux.HandleFunc("/vpn/vpnservices", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("name"); got != "vpn1" {
			t.Errorf("lookup name = %q", got)
		}
		writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{"vpnservices":[{"id":%q,"name":"vpn1"}]}`, vpnSvcID))
	})
	var deleted int
	fakeServer.Mux.HandleFunc("/vpn/vpnservices/"+vpnSvcID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{"vpnservice":{"id":%q,"name":"vpn1","status":"ACTIVE"}}`, vpnSvcID))
		case http.MethodPut:
			th.TestJSONRequest(t, r, `{"vpnservice":{"name":"vpn2","admin_state_up":true,"description":"x"}}`)
			writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{"vpnservice":{"id":%q,"name":"vpn2"}}`, vpnSvcID))
		case http.MethodDelete:
			deleted++
			w.WriteHeader(http.StatusNoContent)
		}
	})
	client := networkClient(fakeServer)
	ctx := context.Background()
	var buf bytes.Buffer
	if err := runVPNServiceShow(ctx, client, vpnaasCSVOut(), "vpn1", &buf); err != nil {
		t.Fatalf("show: %v", err)
	}
	want := "Field,Value\nDescription,\nExt v4 IP,\nExt v6 IP,\nFlavor,\nID," + vpnSvcID +
		"\nName,vpn1\nProject,\nRouter,\nState,false\nStatus,ACTIVE\nSubnet,\n"
	if buf.String() != want {
		t.Errorf("show output =\n%s\nwant\n%s", buf.String(), want)
	}
	buf.Reset()
	if err := runVPNServiceSet(ctx, client, vpnaasCSVOut(), vpnSvcID, &vpnServiceFlags{name: "vpn2", enable: true, description: "x"}, &buf); err != nil {
		t.Fatalf("set: %v", err)
	}
	if !strings.Contains(buf.String(), "Name,vpn2") {
		t.Errorf("set output:\n%s", buf.String())
	}
	if err := runVPNServiceSet(ctx, client, vpnaasCSVOut(), vpnSvcID, &vpnServiceFlags{}, &buf); err == nil {
		t.Error("set with no attribute flag should fail")
	}
	buf.Reset()
	if err := runVPNServiceDelete(ctx, client, []string{vpnSvcID}, &buf); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted != 1 || buf.String() != "Deleted VPN service "+vpnSvcID+"\n" {
		t.Errorf("deleted=%d output=%q", deleted, buf.String())
	}
}

// A cloud without the VPNaaS plugin answers 404 on every VPN path; the error
// must say the service is not deployed rather than read as a missing resource.
func TestRunVPNServiceList_NamesTheMissingPlugin(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"extensions":[{"alias":"router"}]}`)
	})
	fakeServer.Mux.HandleFunc("/vpn/vpnservices/"+vpnSvcID, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, `{"NeutronError":{"message":"not found"}}`)
	})
	err := runVPNServiceShow(context.Background(), networkClient(fakeServer), vpnaasCSVOut(), vpnSvcID, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does not enable the vpnaas extension") {
		t.Fatalf("err = %v, want the vpnaas extension named", err)
	}
}

// ---- policies ----

func TestRunIKEPolicyCreate_LowercasesChoicesAndParsesLifetime(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	vpnWrite(t, fakeServer, "/vpn/ikepolicies", http.MethodPost, fmt.Sprintf(`{"ikepolicy":{
		"name":"ike1","description":"d","auth_algorithm":"sha256","encryption_algorithm":"aes-256",
		"phase1_negotiation_mode":"aggressive","ike_version":"v2","pfs":"group14",
		"lifetime":{"units":"seconds","value":7200},"project_id":%q}}`, vpnaasProjID),
		fmt.Sprintf(`{"ikepolicy":{"id":%q,"name":"ike1","auth_algorithm":"sha256","ike_version":"v2",
		  "lifetime":{"units":"seconds","value":7200}}}`, vpnIKEID), http.StatusCreated)
	choices := ikePolicyChoices()
	for _, c := range choices {
		c.value = map[string]string{
			"auth-algorithm": "SHA256", "encryption-algorithm": "AES-256",
			"phase1-negotiation-mode": "Aggressive", "ike-version": "V2", "pfs": "GROUP14",
		}[c.flag]
	}
	f := &vpnPolicyFlags{name: "ike1", description: "d", choices: choices,
		lifetime: []string{"units=seconds", "value=7200"}, projectID: vpnaasProjID}
	var buf bytes.Buffer
	if err := runIKEPolicyCreate(context.Background(), networkClient(fakeServer), vpnaasCSVOut(), f, &buf); err != nil {
		t.Fatalf("runIKEPolicyCreate: %v", err)
	}
	want := "Field,Value\nAuthentication Algorithm,sha256\nDescription,\nEncryption Algorithm,\nID," + vpnIKEID +
		"\nIKE Version,v2\nLifetime,\"{\"\"units\"\":\"\"seconds\"\",\"\"value\"\":7200}\"\nName,ike1\n" +
		"Perfect Forward Secrecy (PFS),\nPhase1 Negotiation Mode,\nProject,\n"
	if buf.String() != want {
		t.Errorf("output =\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestVPNPolicyAttrs_RejectsBadValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    *vpnPolicyFlags
		want string
	}{
		{"bad unit", &vpnPolicyFlags{lifetime: []string{"units=minutes"}}, "only supported unit"},
		{"short lifetime", &vpnPolicyFlags{lifetime: []string{"value=59"}}, "at least 60"},
		{"unknown key", &vpnPolicyFlags{lifetime: []string{"size=1"}}, "unknown key"},
		{"no equals", &vpnPolicyFlags{lifetime: []string{"seconds"}}, "key=value"},
		{"bad choice", &vpnPolicyFlags{choices: []*vpnChoice{{flag: "pfs", attr: "pfs", choices: vpnPFSGroups, value: "group1"}}}, "must be one of"},
	} {
		if _, err := vpnPolicyAttrs(tc.f); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

// gophercloud's ikepolicies.UpdateOpts spells the key phase_1_negotiation_mode;
// the body must carry neutron's phase1_negotiation_mode.
func TestRunIKEPolicySet_SendsNeutronsPhase1Key(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	calls := vpnWrite(t, fakeServer, "/vpn/ikepolicies/"+vpnIKEID, http.MethodPut,
		`{"ikepolicy":{"phase1_negotiation_mode":"main","name":"ike2"}}`,
		fmt.Sprintf(`{"ikepolicy":{"id":%q,"name":"ike2"}}`, vpnIKEID), http.StatusOK)
	choices := ikePolicyChoices()
	for _, c := range choices {
		if c.flag == "phase1-negotiation-mode" {
			c.value = "main"
		}
	}
	var buf bytes.Buffer
	if err := runIKEPolicySet(context.Background(), networkClient(fakeServer), vpnaasCSVOut(), vpnIKEID,
		&vpnPolicyFlags{name: "ike2", choices: choices}, &buf); err != nil {
		t.Fatalf("runIKEPolicySet: %v", err)
	}
	if *calls != 1 {
		t.Errorf("PUT calls = %d", *calls)
	}
}

func TestRunIKEPolicyListShowDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/vpn/ikepolicies", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{"ikepolicies":[{"id":%q,"name":"ike1","auth_algorithm":"sha1",
		  "encryption_algorithm":"aes-128","ike_version":"v1","pfs":"group5","description":"d",
		  "phase1_negotiation_mode":"main","project_id":%q,"lifetime":{"units":"seconds","value":3600}}]}`, vpnIKEID, vpnaasProjID))
	})
	fakeServer.Mux.HandleFunc("/vpn/ikepolicies/"+vpnIKEID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{"ikepolicy":{"id":%q,"name":"ike1"}}`, vpnIKEID))
	})
	client := networkClient(fakeServer)
	ctx := context.Background()
	var buf bytes.Buffer
	if err := runIKEPolicyList(ctx, client, vpnaasCSVOut(), true, &buf); err != nil {
		t.Fatalf("list: %v", err)
	}
	wantHeader := "ID,Name,Authentication Algorithm,Encryption Algorithm,IKE Version,Perfect Forward Secrecy (PFS)," +
		"Description,Phase1 Negotiation Mode,Project,Lifetime"
	if firstLine(buf.String()) != wantHeader {
		t.Errorf("header = %s", firstLine(buf.String()))
	}
	if !strings.Contains(buf.String(), vpnIKEID+",ike1,sha1,aes-128,v1,group5,d,main,"+vpnaasProjID) {
		t.Errorf("row missing:\n%s", buf.String())
	}
	buf.Reset()
	if err := runIKEPolicyShow(ctx, client, vpnaasCSVOut(), "ike1", &buf); err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(buf.String(), "Lifetime,\n") {
		t.Errorf("absent lifetime should render empty:\n%s", buf.String())
	}
	buf.Reset()
	if err := runIKEPolicyDelete(ctx, client, []string{vpnIKEID}, &buf); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestRunIPsecPolicyCreateSetListShowDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/vpn/ipsecpolicies", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			th.TestJSONRequest(t, r, `{"ipsecpolicy":{"name":"ips1","auth_algorithm":"sha512",
			  "encapsulation_mode":"transport","encryption_algorithm":"aes-128-gcm-16","pfs":"group19",
			  "transform_protocol":"ah-esp","lifetime":{"value":600}}}`)
			writeJSON(t, w, http.StatusCreated, fmt.Sprintf(`{"ipsecpolicy":{"id":%q,"name":"ips1"}}`, vpnIPsecID))
			return
		}
		writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{"ipsecpolicies":[{"id":%q,"name":"ips1","auth_algorithm":"sha1",
		  "encapsulation_mode":"tunnel","transform_protocol":"esp","encryption_algorithm":"aes-128","pfs":"group5",
		  "description":"d","project_id":%q,"lifetime":{"units":"seconds","value":3600}}]}`, vpnIPsecID, vpnaasProjID))
	})
	fakeServer.Mux.HandleFunc("/vpn/ipsecpolicies/"+vpnIPsecID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			th.TestJSONRequest(t, r, `{"ipsecpolicy":{"description":"new","encapsulation_mode":"tunnel"}}`)
			fallthrough
		case http.MethodGet:
			writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{"ipsecpolicy":{"id":%q,"name":"ips1","transform_protocol":"esp"}}`, vpnIPsecID))
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	client := networkClient(fakeServer)
	ctx := context.Background()
	values := map[string]string{
		"auth-algorithm": "sha512", "encapsulation-mode": "TRANSPORT", "encryption-algorithm": "aes-128-gcm-16",
		"pfs": "group19", "transform-protocol": "AH-ESP",
	}
	choices := ipsecPolicyChoices()
	for _, c := range choices {
		c.value = values[c.flag]
	}
	var buf bytes.Buffer
	if err := runIPsecPolicyCreate(ctx, client, vpnaasCSVOut(), &vpnPolicyFlags{name: "ips1", choices: choices, lifetime: []string{"value=600"}}, &buf); err != nil {
		t.Fatalf("create: %v", err)
	}
	want := "Field,Value\nAuthentication Algorithm,\nDescription,\nEncapsulation Mode,\nEncryption Algorithm,\nID," +
		vpnIPsecID + "\nLifetime,\nName,ips1\nPerfect Forward Secrecy (PFS),\nProject,\nTransform Protocol,\n"
	if buf.String() != want {
		t.Errorf("create output =\n%s\nwant\n%s", buf.String(), want)
	}
	setChoices := ipsecPolicyChoices()
	setChoices[1].value = "tunnel"
	buf.Reset()
	if err := runIPsecPolicySet(ctx, client, vpnaasCSVOut(), vpnIPsecID, &vpnPolicyFlags{description: "new", choices: setChoices}, &buf); err != nil {
		t.Fatalf("set: %v", err)
	}
	buf.Reset()
	if err := runIPsecPolicyList(ctx, client, vpnaasCSVOut(), false, &buf); err != nil {
		t.Fatalf("list: %v", err)
	}
	wantList := "ID,Name,Authentication Algorithm,Encapsulation Mode,Transform Protocol,Encryption Algorithm\n" +
		vpnIPsecID + ",ips1,sha1,tunnel,esp,aes-128\n"
	if buf.String() != wantList {
		t.Errorf("list =\n%s\nwant\n%s", buf.String(), wantList)
	}
	buf.Reset()
	if err := runIPsecPolicyList(ctx, client, vpnaasCSVOut(), true, &buf); err != nil {
		t.Fatalf("list --long: %v", err)
	}
	if !strings.HasSuffix(firstLine(buf.String()), ",Perfect Forward Secrecy (PFS),Description,Project,Lifetime") {
		t.Errorf("long header = %s", firstLine(buf.String()))
	}
	buf.Reset()
	if err := runIPsecPolicyShow(ctx, client, vpnaasCSVOut(), vpnIPsecID, &buf); err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(buf.String(), "Transform Protocol,esp") {
		t.Errorf("show:\n%s", buf.String())
	}
	if err := runIPsecPolicyDelete(ctx, client, []string{vpnIPsecID}, &buf); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// ---- endpoint groups ----

func TestRunVPNEndpointGroupCreate_ResolvesSubnetsOnly(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	lookups := 0
	fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, r *http.Request) {
		lookups++
		id := map[string]string{"a": vpnSubID, "b": vpnSub2ID}[r.URL.Query().Get("name")]
		writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{"subnets":[{"id":%q}]}`, id))
	})
	var body string
	fakeServer.Mux.HandleFunc("/vpn/endpoint-groups", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, body)
		writeJSON(t, w, http.StatusCreated, fmt.Sprintf(`{"endpoint_group":{"id":%q,"name":"g","type":"subnet",
		  "endpoints":[%q,%q]}}`, vpnEPGLID, vpnSubID, vpnSub2ID))
	})
	client := networkClient(fakeServer)
	ctx := context.Background()

	body = fmt.Sprintf(`{"endpoint_group":{"name":"g","description":"d","type":"subnet","endpoints":[%q,%q],"project_id":%q}}`,
		vpnSubID, vpnSub2ID, vpnaasProjID)
	var buf bytes.Buffer
	if err := runVPNEndpointGroupCreate(ctx, client, vpnaasCSVOut(), &vpnEndpointGroupFlags{
		name: "g", description: "d", typ: "Subnet", values: []string{"a", "b"}, projectID: vpnaasProjID,
	}, &buf); err != nil {
		t.Fatalf("create subnet: %v", err)
	}
	want := "Field,Value\nDescription,\nEndpoints,\"" + vpnSubID + ", " + vpnSub2ID + "\"\nID," + vpnEPGLID +
		"\nName,g\nProject,\nType,subnet\n"
	if buf.String() != want {
		t.Errorf("output =\n%s\nwant\n%s", buf.String(), want)
	}

	lookups = 0
	body = `{"endpoint_group":{"name":"g","type":"cidr","endpoints":["192.0.2.0/24","198.51.100.0/24"]}}`
	if err := runVPNEndpointGroupCreate(ctx, client, vpnaasCSVOut(), &vpnEndpointGroupFlags{
		name: "g", typ: "cidr", values: []string{"192.0.2.0/24", "198.51.100.0/24"},
	}, &buf); err != nil {
		t.Fatalf("create cidr: %v", err)
	}
	if lookups != 0 {
		t.Errorf("cidr endpoints were looked up as subnets (%d lookups)", lookups)
	}
	if err := runVPNEndpointGroupCreate(ctx, client, vpnaasCSVOut(), &vpnEndpointGroupFlags{name: "g", typ: "network", values: []string{"x"}}, &buf); err == nil {
		t.Error("--type network should be refused")
	}
}

func TestRunVPNEndpointGroupListShowSetDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/vpn/endpoint-groups", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{"endpoint_groups":[{"id":%q,"name":"g","type":"cidr",
		  "endpoints":["192.0.2.0/24"],"description":"d","project_id":%q}]}`, vpnEPGLID, vpnaasProjID))
	})
	fakeServer.Mux.HandleFunc("/vpn/endpoint-groups/"+vpnEPGLID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			th.TestJSONRequest(t, r, `{"endpoint_group":{"name":"g2","description":"x"}}`)
			fallthrough
		case http.MethodGet:
			writeJSON(t, w, http.StatusOK, fmt.Sprintf(`{"endpoint_group":{"id":%q,"name":"g2"}}`, vpnEPGLID))
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	client := networkClient(fakeServer)
	ctx := context.Background()
	var buf bytes.Buffer
	if err := runVPNEndpointGroupList(ctx, client, vpnaasCSVOut(), true, &buf); err != nil {
		t.Fatalf("list: %v", err)
	}
	want := "ID,Name,Type,Endpoints,Description,Project\n" + vpnEPGLID + ",g,cidr,192.0.2.0/24,d," + vpnaasProjID + "\n"
	if buf.String() != want {
		t.Errorf("list =\n%s\nwant\n%s", buf.String(), want)
	}
	buf.Reset()
	if err := runVPNEndpointGroupShow(ctx, client, vpnaasCSVOut(), "g", &buf); err != nil {
		t.Fatalf("show: %v", err)
	}
	if err := runVPNEndpointGroupSet(ctx, client, vpnaasCSVOut(), vpnEPGLID, &vpnEndpointGroupFlags{name: "g2", description: "x"}, &buf); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := runVPNEndpointGroupDelete(ctx, client, []string{vpnEPGLID}, &buf); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// Endpoint groups are their own extension on top of vpnaas.
func TestRunVPNEndpointGroupList_NamesTheMissingExtension(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"extensions":[{"alias":"vpnaas"}]}`)
	})
	fakeServer.Mux.HandleFunc("/vpn/endpoint-groups", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, `{"NeutronError":{"message":"not found"}}`)
	})
	err := runVPNEndpointGroupList(context.Background(), networkClient(fakeServer), vpnaasCSVOut(), false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does not enable the vpn-endpoint-groups extension") ||
		strings.Contains(err.Error(), "the vpnaas extension") {
		t.Fatalf("err = %v", err)
	}
}

// ---- IPsec site connections ----

func TestRunIPsecSiteConnectionCreate_SendsUpstreamBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	vpnWrite(t, fakeServer, "/vpn/ipsec-site-connections", http.MethodPost, fmt.Sprintf(`{"ipsec_site_connection":{
		"name":"c1","description":"d","mtu":1400,"admin_state_up":true,"initiator":"response-only",
		"dpd":{"action":"hold","interval":30,"timeout":120},
		"local_ep_group_id":%q,"peer_ep_group_id":%q,"local_id":"lid",
		"vpnservice_id":%q,"ikepolicy_id":%q,"ipsecpolicy_id":%q,
		"peer_id":"192.0.2.10","peer_address":"192.0.2.10","psk":"secret","project_id":%q}}`,
		vpnEPGLID, vpnEPGPID, vpnSvcID, vpnIKEID, vpnIPsecID, vpnaasProjID),
		fmt.Sprintf(`{"ipsec_site_connection":{"id":%q,"name":"c1","peer_address":"192.0.2.10","auth_mode":"psk",
		  "status":"PENDING_CREATE","dpd":{"action":"hold","interval":30,"timeout":120},"mtu":1400,
		  "peer_cidrs":[],"admin_state_up":true}}`, vpnConnID), http.StatusCreated)
	f := newIPsecSiteConnectionFlags()
	f.initiator[0].value = "Response-Only"
	f.name, f.description, f.mtu, f.enable = "c1", "d", "1400", true
	f.dpd = []string{"action=hold,interval=30", "timeout=120"}
	f.localEndpointGroup, f.peerEndpointGroup, f.localID = vpnEPGLID, vpnEPGPID, "lid"
	f.vpnService, f.ikePolicy, f.ipsecPolicy = vpnSvcID, vpnIKEID, vpnIPsecID
	f.peerID, f.peerAddress, f.psk, f.projectID = "192.0.2.10", "192.0.2.10", "secret", vpnaasProjID
	var buf bytes.Buffer
	if err := runIPsecSiteConnectionCreate(context.Background(), networkClient(fakeServer), vpnaasCSVOut(), f, &buf); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, want := range []string{
		"Authentication Algorithm,psk", "DPD,\"{\"\"action\"\":\"\"hold\"\",\"\"interval\"\":30,\"\"timeout\"\":120}\"",
		"MTU,1400", "State,true", "Status,PENDING_CREATE", "VPN Service,",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, buf.String())
		}
	}
}

func TestRunIPsecSiteConnectionCreate_UpstreamCrossFlagChecks(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	client := networkClient(fakeServer)
	for _, tc := range []struct {
		name string
		mod  func(*ipsecSiteConnectionFlags)
		want string
	}{
		{"one endpoint group", func(f *ipsecSiteConnectionFlags) { f.localEndpointGroup = vpnEPGLID }, "both --local-endpoint-group"},
		{"neither groups nor cidrs", func(*ipsecSiteConnectionFlags) {}, "endpoint groups or --peer-cidr"},
		{"bad dpd action", func(f *ipsecSiteConnectionFlags) {
			f.peerCIDRs = []string{"192.0.2.0/24"}
			f.dpd = []string{"action=never"}
		}, "must be one of"},
		{"bad dpd interval", func(f *ipsecSiteConnectionFlags) {
			f.peerCIDRs = []string{"192.0.2.0/24"}
			f.dpd = []string{"interval=0"}
		}, "positive integer"},
		{"bad mtu", func(f *ipsecSiteConnectionFlags) {
			f.peerCIDRs = []string{"192.0.2.0/24"}
			f.mtu = "big"
		}, "--mtu"},
	} {
		f := newIPsecSiteConnectionFlags()
		tc.mod(f)
		err := runIPsecSiteConnectionCreate(context.Background(), client, vpnaasCSVOut(), f, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestRunIPsecSiteConnectionListShowSetDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	conn := fmt.Sprintf(`{"id":%q,"name":"c1","peer_address":"192.0.2.10","auth_mode":"psk","status":"ACTIVE",
	  "project_id":%q,"peer_cidrs":["192.0.2.0/24","198.51.100.0/24"],"vpnservice_id":%q,"ipsecpolicy_id":%q,
	  "ikepolicy_id":%q,"mtu":1500,"initiator":"bi-directional","admin_state_up":true,"description":"d",
	  "psk":"secret","route_mode":"static","local_id":"","peer_id":"192.0.2.10","local_ep_group_id":null,
	  "peer_ep_group_id":null,"dpd":{"action":"hold","interval":30,"timeout":120}}`,
		vpnConnID, vpnaasProjID, vpnSvcID, vpnIPsecID, vpnIKEID)
	fakeServer.Mux.HandleFunc("/vpn/ipsec-site-connections", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"ipsec_site_connections":[`+conn+`]}`)
	})
	fakeServer.Mux.HandleFunc("/vpn/ipsec-site-connections/"+vpnConnID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			th.TestJSONRequest(t, r, `{"ipsec_site_connection":{"peer_cidrs":["203.0.113.0/24"],"admin_state_up":false,
			  "peer_address":"203.0.113.1","name":"c2"}}`)
			fallthrough
		case http.MethodGet:
			writeJSON(t, w, http.StatusOK, `{"ipsec_site_connection":`+conn+`}`)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	client := networkClient(fakeServer)
	ctx := context.Background()
	var buf bytes.Buffer
	if err := runIPsecSiteConnectionList(ctx, client, vpnaasCSVOut(), false, &buf); err != nil {
		t.Fatalf("list: %v", err)
	}
	want := "ID,Name,Peer Address,Authentication Algorithm,Status\n" + vpnConnID + ",c1,192.0.2.10,psk,ACTIVE\n"
	if buf.String() != want {
		t.Errorf("list =\n%s\nwant\n%s", buf.String(), want)
	}
	buf.Reset()
	if err := runIPsecSiteConnectionList(ctx, client, vpnaasCSVOut(), true, &buf); err != nil {
		t.Fatalf("list --long: %v", err)
	}
	wantHeader := "ID,Name,Peer Address,Authentication Algorithm,Status,Project,Peer CIDRs,VPN Service,IPSec Policy," +
		"IKE Policy,MTU,Initiator,State,Description,Pre-shared Key,Route Mode,Local ID,Peer ID," +
		"Local Endpoint Group ID,Peer Endpoint Group ID,DPD"
	if firstLine(buf.String()) != wantHeader {
		t.Errorf("long header = %s", firstLine(buf.String()))
	}
	if !strings.Contains(buf.String(), `"192.0.2.0/24, 198.51.100.0/24"`) {
		t.Errorf("peer CIDRs not joined:\n%s", buf.String())
	}
	buf.Reset()
	if err := runIPsecSiteConnectionShow(ctx, client, vpnaasCSVOut(), vpnConnID, &buf); err != nil {
		t.Fatalf("show: %v", err)
	}
	var keys []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n")[1:] {
		keys = append(keys, strings.SplitN(line, ",", 2)[0])
	}
	wantKeys := "Authentication Algorithm|DPD|Description|ID|IKE Policy|IPSec Policy|Initiator|Local Endpoint Group ID|" +
		"Local ID|MTU|Name|Peer Address|Peer CIDRs|Peer Endpoint Group ID|Peer ID|Pre-shared Key|Project|Route Mode|" +
		"State|Status|VPN Service"
	if strings.Join(keys, "|") != wantKeys {
		t.Errorf("show fields = %s", strings.Join(keys, "|"))
	}
	f := newIPsecSiteConnectionFlags()
	f.peerCIDRs, f.disable, f.peerAddress, f.name = []string{"203.0.113.0/24"}, true, "203.0.113.1", "c2"
	if err := runIPsecSiteConnectionSet(ctx, client, vpnaasCSVOut(), vpnConnID, f, &buf); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := runIPsecSiteConnectionSet(ctx, client, vpnaasCSVOut(), vpnConnID, newIPsecSiteConnectionFlags(), &buf); err == nil {
		t.Error("set with no attribute flag should fail")
	}
	if err := runIPsecSiteConnectionDelete(ctx, client, []string{vpnConnID}, &buf); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// ---- cobra wiring ----

// The nested nouns resolve to upstream's exact paths and their flags reach the
// request body.
func TestExec_VPNSiteConnectionCreate_FlagsReachTheBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	calls := vpnWrite(t, fakeServer, "/v2.0/vpn/ipsec-site-connections", http.MethodPost, fmt.Sprintf(`{"ipsec_site_connection":{
		"name":"c1","peer_cidrs":["192.0.2.0/24","198.51.100.0/24"],"initiator":"bi-directional",
		"vpnservice_id":%q,"ikepolicy_id":%q,"ipsecpolicy_id":%q,
		"peer_id":"peer","peer_address":"192.0.2.1","psk":"k","dpd":{"action":"clear"}}}`, vpnSvcID, vpnIKEID, vpnIPsecID),
		fmt.Sprintf(`{"ipsec_site_connection":{"id":%q}}`, vpnConnID), http.StatusCreated)
	out, err := execNetwork(t, fakeServer, "vpn", "ipsec", "site", "connection", "create", "c1",
		"--vpnservice", vpnSvcID, "--ikepolicy", vpnIKEID, "--ipsecpolicy", vpnIPsecID,
		"--peer-id", "peer", "--peer-address", "192.0.2.1", "--psk", "k",
		"--peer-cidr", "192.0.2.0/24", "--peer-cidr", "198.51.100.0/24",
		"--initiator", "bi-directional", "--dpd", "action=clear")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if *calls != 1 {
		t.Errorf("POST calls = %d", *calls)
	}
	if _, err := execNetwork(t, fakeServer, "vpn", "ipsec", "site", "connection", "create", "c1",
		"--vpnservice", vpnSvcID, "--ikepolicy", vpnIKEID, "--ipsecpolicy", vpnIPsecID, "--peer-id", "p",
		"--peer-address", "a", "--psk", "k", "--peer-cidr", "192.0.2.0/24", "--local-endpoint-group", vpnEPGLID); err == nil {
		t.Error("--peer-cidr with --local-endpoint-group should be refused")
	}
	if _, err := execNetwork(t, fakeServer, "vpn", "ipsec", "site", "connection", "create", "c1", "--peer-cidr", "192.0.2.0/24"); err == nil ||
		!strings.Contains(err.Error(), "required") {
		t.Errorf("missing required flags: err = %v", err)
	}
}

func TestExec_VPNNounsReachTheirPaths(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	vpnWrite(t, fakeServer, "/v2.0/vpn/ikepolicies/"+vpnIKEID, http.MethodPut,
		`{"ikepolicy":{"ike_version":"v1","lifetime":{"units":"seconds","value":3600}}}`,
		fmt.Sprintf(`{"ikepolicy":{"id":%q}}`, vpnIKEID), http.StatusOK)
	vpnWrite(t, fakeServer, "/v2.0/vpn/ipsecpolicies", http.MethodPost,
		`{"ipsecpolicy":{"name":"p","transform_protocol":"ah"}}`,
		fmt.Sprintf(`{"ipsecpolicy":{"id":%q}}`, vpnIPsecID), http.StatusCreated)
	vpnWrite(t, fakeServer, "/v2.0/vpn/endpoint-groups", http.MethodPost,
		`{"endpoint_group":{"name":"g","type":"cidr","endpoints":["192.0.2.0/24"]}}`,
		fmt.Sprintf(`{"endpoint_group":{"id":%q}}`, vpnEPGLID), http.StatusCreated)
	vpnWrite(t, fakeServer, "/v2.0/vpn/vpnservices/"+vpnSvcID, http.MethodPut,
		`{"vpnservice":{"admin_state_up":false}}`,
		fmt.Sprintf(`{"vpnservice":{"id":%q}}`, vpnSvcID), http.StatusOK)
	for _, argv := range [][]string{
		{"vpn", "ike", "policy", "set", vpnIKEID, "--ike-version", "v1", "--lifetime", "units=seconds,value=3600"},
		{"vpn", "ipsec", "policy", "create", "p", "--transform-protocol", "AH"},
		{"vpn", "endpoint", "group", "create", "g", "--type", "cidr", "--value", "192.0.2.0/24"},
		{"vpn", "service", "set", vpnSvcID, "--disable"},
	} {
		if out, err := execNetwork(t, fakeServer, argv...); err != nil {
			t.Errorf("%s: %v\n%s", strings.Join(argv, " "), err, out)
		}
	}
	if _, err := execNetwork(t, fakeServer, "vpn", "service", "set", vpnSvcID, "--enable", "--disable"); err == nil {
		t.Error("--enable with --disable should be refused")
	}
}
