package network

import (
	"bytes"
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/rules"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const (
	sgTestProjectID = "11111111-2222-3333-4444-555555555555"
	sgTestGroupID   = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	sgTestAddrGrpID = "99999999-8888-7777-6666-555555555555"
)

func intPtr(n int) *int { return &n }

// --- security group ---------------------------------------------------------

// Upstream defaults an omitted --description to the name, sends stateful only
// when --stateful/--stateless is given, and sets tags after the create.
func TestRunSecurityGroupCreate_StatelessProjectExtraThenTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/security-groups", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"security_group":{
		  "name":"web","description":"web","stateful":false,"project_id":"p1","custom":"x"}}`)
		writeJSON(t, w, http.StatusCreated, `{"security_group":{"id":"sg-1","name":"web","description":"web",
		  "stateful":false,"shared":false,"revision_number":1,"tags":[]}}`)
	})
	fakeServer.Mux.HandleFunc("/security-groups/sg-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["a","b"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["a","b"]}`)
	})

	f := &secGroupCreateFlags{
		stateless: true, projectID: "p1",
		extraProperty: []string{"name=custom,value=x"},
		tagWriteFlags: tagWriteFlags{tags: []string{"b", "a"}},
	}
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runSecurityGroupCreate(context.Background(), networkClient(fakeServer), o, "web", f,
		fakeFlags{flagSGStateless: true}, &buf); err != nil {
		t.Fatalf("runSecurityGroupCreate: %v", err)
	}
	for _, want := range []string{`"stateful": false`, `"shared": false`, `"revision_number": 1`, `"a"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %s:\n%s", want, buf.String())
		}
	}
}

func TestRunSecurityGroupCreate_StatefulAndExplicitEmptyDescription(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/security-groups", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		// An explicit --description "" is neutron's default and so omitted.
		th.TestJSONRequest(t, r, `{"security_group":{"name":"web","stateful":true}}`)
		writeJSON(t, w, http.StatusCreated, `{"security_group":{"id":"sg-1","name":"web"}}`)
	})
	f := &secGroupCreateFlags{stateful: true}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runSecurityGroupCreate(context.Background(), networkClient(fakeServer), o, "web", f,
		fakeFlags{flagSGStateful: true, flagDescription: true}, &buf); err != nil {
		t.Fatalf("runSecurityGroupCreate: %v", err)
	}
}

func TestRunSecurityGroupSet_StatefulAndExtraProperty(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/security-groups", "security_groups")
	fakeServer.Mux.HandleFunc("/security-groups/sg-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"security_group":{"stateful":true,"custom":3}}`)
		writeJSON(t, w, http.StatusOK, `{"security_group":{"id":"sg-1","stateful":true}}`)
	})
	f := &secGroupSetFlags{stateful: true, extraProperty: []string{"type=int,name=custom,value=3"}}
	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	if err := runSecurityGroupSet(context.Background(), networkClient(fakeServer), o, "sg-1", f,
		fakeFlags{flagSGStateful: true}, &buf); err != nil {
		t.Fatalf("runSecurityGroupSet: %v", err)
	}
}

// Upstream sends no update when tags are the only change; the current tags
// come from a GET instead, and --no-tag --tag overwrites the set.
func TestRunSecurityGroupSet_TagsOnlySkipsTheUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/security-groups", "security_groups")
	fakeServer.Mux.HandleFunc("/security-groups/sg-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		writeJSON(t, w, http.StatusOK, `{"security_group":{"id":"sg-1","tags":["old"]}}`)
	})
	fakeServer.Mux.HandleFunc("/security-groups/sg-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":["new"]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":["new"]}`)
	})
	f := &secGroupSetFlags{tagWriteFlags: tagWriteFlags{tags: []string{"new"}, noTag: true}}
	o := &output.Options{Format: output.FormatValue, Columns: []string{"tags"}}
	var buf bytes.Buffer
	if err := runSecurityGroupSet(context.Background(), networkClient(fakeServer), o, "sg-1", f, fakeFlags{}, &buf); err != nil {
		t.Fatalf("runSecurityGroupSet: %v", err)
	}
	if strings.Contains(buf.String(), "old") || !strings.Contains(buf.String(), "new") {
		t.Errorf("output does not show the overwritten tag set:\n%s", buf.String())
	}
}

func TestRunSecurityGroupSet_NoFlagIsAnError(t *testing.T) {
	// The check comes before any request, so no client is needed.
	err := runSecurityGroupSet(context.Background(), nil, &output.Options{}, "sg-1", &secGroupSetFlags{}, fakeFlags{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("err = %v, want an at-least-one-flag error", err)
	}
}

func TestRunSecurityGroupUnset_TagAndAllTag(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags tagWriteFlags
		want  string
	}{
		{"tag", tagWriteFlags{tags: []string{"b"}}, `{"tags":["a","c"]}`},
		{"all-tag", tagWriteFlags{allTag: true}, `{"tags":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			emptyLookup(t, fakeServer, "/security-groups", "security_groups")
			fakeServer.Mux.HandleFunc("/security-groups/sg-1", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodGet)
				writeJSON(t, w, http.StatusOK, `{"security_group":{"id":"sg-1","tags":["a","b","c"]}}`)
			})
			fakeServer.Mux.HandleFunc("/security-groups/sg-1/tags", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodPut)
				th.TestJSONRequest(t, r, tc.want)
				writeJSON(t, w, http.StatusOK, tc.want)
			})
			o := &output.Options{Format: output.FormatValue}
			var buf bytes.Buffer
			if err := runSecurityGroupUnset(context.Background(), networkClient(fakeServer), o, "sg-1", &tc.flags, &buf); err != nil {
				t.Fatalf("runSecurityGroupUnset: %v", err)
			}
		})
	}
}

func TestRunSecurityGroupUnset_RequiresAFlag(t *testing.T) {
	err := runSecurityGroupUnset(context.Background(), nil, &output.Options{}, "sg-1", &tagWriteFlags{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--tag") {
		t.Fatalf("err = %v, want a --tag/--all-tag requirement", err)
	}
}

func TestRunSecurityGroupShow_SharedAndRevision(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/security-groups", "security_groups")
	fakeServer.Mux.HandleFunc("/security-groups/sg-1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"security_group":{"id":"sg-1","name":"web","stateful":true,"shared":true,"revision_number":4}}`)
	})
	o := &output.Options{Format: output.FormatValue, Columns: []string{"stateful", "shared", "revision_number"}}
	var buf bytes.Buffer
	if err := runSecurityGroupShow(context.Background(), networkClient(fakeServer), o, "sg-1", &buf); err != nil {
		t.Fatalf("runSecurityGroupShow: %v", err)
	}
	if got, want := buf.String(), "true\ntrue\n4\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// --- security group rule create ---------------------------------------------

// --icmp-type/--icmp-code map onto port_range_min/max, and 0 is a real value
// (echo reply) that has to reach the body even though CreateOpts would drop it.
func TestRunSecurityGroupRuleCreate_ICMPTypeCodeZeroAndNewAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/security-groups", "security_groups")
	emptyLookup(t, fakeServer, "/address-groups", "address_groups")
	fakeServer.Mux.HandleFunc("/security-group-rules", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"security_group_rule":{
		  "direction":"egress","ethertype":"IPv4","protocol":"icmp","security_group_id":"sg-1",
		  "port_range_min":0,"port_range_max":0,"description":"ping replies",
		  "remote_address_group_id":"ag-1","project_id":"p1","custom":true}}`)
		writeJSON(t, w, http.StatusCreated, `{"security_group_rule":{"id":"rule-1","protocol":"icmp",
		  "remote_address_group_id":"ag-1","description":"ping replies"}}`)
	})
	f := &secGroupRuleCreateFlags{
		protocol: "ICMP", egress: true, icmpType: intPtr(0), icmpCode: intPtr(0),
		description: "ping replies", remoteAddressGroup: "ag-1", projectID: "p1",
		extraProperty: []string{"type=bool,name=custom,value=True"},
	}
	o := &output.Options{Format: output.FormatJSON}
	var buf bytes.Buffer
	if err := runSecurityGroupRuleCreate(context.Background(), networkClient(fakeServer), o, "sg-1", f, &buf); err != nil {
		t.Fatalf("runSecurityGroupRuleCreate: %v", err)
	}
	if !strings.Contains(buf.String(), `"remote_address_group_id": "ag-1"`) {
		t.Errorf("output missing remote_address_group_id:\n%s", buf.String())
	}
}

// "any" means no protocol; a numeric IPv6-only protocol implies IPv6; with no
// remote, upstream sends the ethertype's match-all prefix; --dst-port is
// ignored for an ICMP protocol; a negative ICMP type is accepted and not sent.
func TestRunSecurityGroupRuleCreate_ProtocolNormalisation(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    secGroupRuleCreateFlags
		body string
	}{
		{"any", secGroupRuleCreateFlags{protocol: "ANY", dstPort: "22"},
			`{"direction":"ingress","ethertype":"IPv4","security_group_id":"sg-1","remote_ip_prefix":"0.0.0.0/0",
			  "port_range_min":22,"port_range_max":22}`},
		{"numeric-ipv6", secGroupRuleCreateFlags{protocol: "58", icmpType: intPtr(128)},
			`{"direction":"ingress","ethertype":"IPv6","protocol":"58","security_group_id":"sg-1",
			  "remote_ip_prefix":"::/0","port_range_min":128}`},
		{"dst-port-ignored-for-icmp", secGroupRuleCreateFlags{protocol: "ipv6-icmp", dstPort: "80"},
			`{"direction":"ingress","ethertype":"IPv6","protocol":"ipv6-icmp","security_group_id":"sg-1","remote_ip_prefix":"::/0"}`},
		{"negative-type", secGroupRuleCreateFlags{protocol: "icmp", icmpType: intPtr(-1), remoteIP: "192.0.2.0/24"},
			`{"direction":"ingress","ethertype":"IPv4","protocol":"icmp","security_group_id":"sg-1","remote_ip_prefix":"192.0.2.0/24"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			emptyLookup(t, fakeServer, "/security-groups", "security_groups")
			fakeServer.Mux.HandleFunc("/security-group-rules", func(w http.ResponseWriter, r *http.Request) {
				th.TestJSONRequest(t, r, `{"security_group_rule":`+tc.body+`}`)
				writeJSON(t, w, http.StatusCreated, `{"security_group_rule":{"id":"rule-1"}}`)
			})
			o := &output.Options{Format: output.FormatValue}
			if err := runSecurityGroupRuleCreate(context.Background(), networkClient(fakeServer), o, "sg-1", &tc.f, &bytes.Buffer{}); err != nil {
				t.Fatalf("runSecurityGroupRuleCreate: %v", err)
			}
		})
	}
}

func TestRunSecurityGroupRuleCreate_ICMPConflicts(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    secGroupRuleCreateFlags
		want string
	}{
		{"dst-port-and-icmp", secGroupRuleCreateFlags{protocol: "icmp", dstPort: "80", icmpType: intPtr(8)}, "cannot be combined"},
		{"code-without-type", secGroupRuleCreateFlags{protocol: "icmp", icmpCode: intPtr(0)}, "requires --icmp-type"},
		{"icmp-on-tcp", secGroupRuleCreateFlags{protocol: "tcp", icmpType: intPtr(8)}, "need an ICMP --protocol"},
		{"icmp-on-any", secGroupRuleCreateFlags{icmpType: intPtr(8)}, "need an ICMP --protocol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The error comes before any request, so no client is needed.
			err := runSecurityGroupRuleCreate(context.Background(), nil, &output.Options{}, "sg-1", &tc.f, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// --- security group rule list / show -----------------------------------------

func TestRunSecurityGroupRuleList_SendsEveryFilterAndUpstreamColumns(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/security-group-rules", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		q := r.URL.Query()
		want := map[string][]string{
			"direction":  {"egress"},
			"ethertype":  {"IPv6"},
			"protocol":   {"ipv6-icmp"},
			"project_id": {"p1"},
		}
		if !reflect.DeepEqual(map[string][]string(q), want) {
			t.Errorf("query = %v, want %v", q, want)
		}
		writeJSON(t, w, http.StatusOK, `{"security_group_rules":[
		  {"id":"r1","direction":"egress","ethertype":"IPv6","protocol":"ipv6-icmp",
		   "port_range_min":128,"port_range_max":0,"security_group_id":"sg-1"},
		  {"id":"r2","direction":"egress","ethertype":"IPv6","protocol":"tcp",
		   "port_range_min":22,"port_range_max":22,"remote_ip_prefix":"2001:db8::/32",
		   "remote_address_group_id":"ag-1","security_group_id":"sg-1"}]}`)
	})
	f := &secGroupRuleListFlags{protocol: "IPv6-ICMP", ethertype: "ipv6", egress: true, long: true}
	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runSecurityGroupRuleList(context.Background(), networkClient(fakeServer), o, "", f, "p1", &buf); err != nil {
		t.Fatalf("runSecurityGroupRuleList: %v", err)
	}
	want := "ID,IP Protocol,Ethertype,IP Range,Port Range,Direction,Remote Security Group,Remote Address Group,Security Group\n" +
		"r1,ipv6-icmp,IPv6,::/0,type=128,egress,,,sg-1\n" +
		"r2,tcp,IPv6,2001:db8::/32,22:22,egress,,ag-1,sg-1\n"
	if got := buf.String(); got != want {
		t.Errorf("output =\n%s\nwant\n%s", got, want)
	}
}

// With a <group> positional the Security Group column is dropped, as upstream.
func TestRunSecurityGroupRuleList_GroupDropsTheGroupColumn(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/security-groups", "security_groups")
	fakeServer.Mux.HandleFunc("/security-group-rules", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"security_group_id": "sg-1", "direction": "ingress"})
		writeJSON(t, w, http.StatusOK, `{"security_group_rules":[]}`)
	})
	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runSecurityGroupRuleList(context.Background(), networkClient(fakeServer), o, "sg-1",
		&secGroupRuleListFlags{ingress: true}, "", &buf); err != nil {
		t.Fatalf("runSecurityGroupRuleList: %v", err)
	}
	want := "ID,IP Protocol,Ethertype,IP Range,Port Range,Direction,Remote Security Group,Remote Address Group"
	if got := strings.TrimSpace(buf.String()); got != want {
		t.Errorf("header = %s\nwant     %s", got, want)
	}
}

func TestSecGroupRulePortRange(t *testing.T) {
	for _, tc := range []struct {
		proto  string
		lo, hi int
		want   string
	}{
		{"icmp", 8, 0, "type=8"},
		{"icmp", 3, 1, "type=3:code=1"},
		{"icmp", 0, 0, ""},
		{"tcp", 80, 80, "80:80"},
		{"udp", 1000, 2000, "1000:2000"},
		{"", 0, 0, ""},
	} {
		r := rules.SecGroupRule{Protocol: tc.proto, PortRangeMin: tc.lo, PortRangeMax: tc.hi}
		if got := secGroupRulePortRange(&r); got != tc.want {
			t.Errorf("port range(%s %d:%d) = %q, want %q", tc.proto, tc.lo, tc.hi, got, tc.want)
		}
	}
}

func TestRunSecurityGroupRuleShow_DefaultPrefixAndAddressGroup(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/security-group-rules/rule-1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"security_group_rule":{"id":"rule-1","ethertype":"IPv6",
		  "remote_ip_prefix":null,"remote_address_group_id":"ag-1"}}`)
	})
	o := &output.Options{Format: output.FormatValue, Columns: []string{"remote_ip_prefix", "remote_address_group_id"}}
	var buf bytes.Buffer
	if err := runSecurityGroupRuleShow(context.Background(), networkClient(fakeServer), o, "rule-1", &buf); err != nil {
		t.Fatalf("runSecurityGroupRuleShow: %v", err)
	}
	if got, want := buf.String(), "::/0\nag-1\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// --- through cobra: the flags wired in RunE reach the seam -------------------

func secGroupLookupAndCreate(t *testing.T, fakeServer th.FakeServer, path, key string, post func(r *http.Request) string) {
	t.Helper()
	fakeServer.Mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, `{"`+key+`":[]}`)
			return
		}
		writeJSON(t, w, http.StatusCreated, post(r))
	})
}

func TestExec_SecurityGroupRuleCreate_ICMPAndProject(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	secGroupLookupAndCreate(t, fakeServer, "/v2.0/security-groups", "security_groups", nil)
	fakeServer.Mux.HandleFunc("/v2.0/security-group-rules", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"security_group_rule":{
		  "direction":"ingress","ethertype":"IPv4","protocol":"icmp","security_group_id":"`+sgTestGroupID+`",
		  "port_range_min":0,"port_range_max":0,"remote_address_group_id":"`+sgTestAddrGrpID+`",
		  "project_id":"`+sgTestProjectID+`","description":"d"}}`)
		writeJSON(t, w, http.StatusCreated, `{"security_group_rule":{"id":"rule-1"}}`)
	})
	out, err := execNetwork(t, fakeServer, "security", "group", "rule", "create", sgTestGroupID,
		"--protocol", "icmp", "--icmp-type", "0", "--icmp-code", "0", "--description", "d",
		"--remote-address-group", sgTestAddrGrpID, "--project", sgTestProjectID)
	if err != nil {
		t.Fatalf("security group rule create: %v (%s)", err, out)
	}
}

func TestExec_SecurityGroupRuleCreate_RemoteFlagsAreExclusive(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	_, err := execNetwork(t, fakeServer, "security", "group", "rule", "create", sgTestGroupID,
		"--remote-ip", "192.0.2.0/24", "--remote-address-group", sgTestAddrGrpID)
	if err == nil || !strings.Contains(err.Error(), "none of the others can be") {
		t.Fatalf("err = %v, want a mutual-exclusion error", err)
	}
}

func TestExec_SecurityGroupCreate_StatelessProjectTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	secGroupLookupAndCreate(t, fakeServer, "/v2.0/security-groups", "security_groups", func(r *http.Request) string {
		th.TestJSONRequest(t, r, `{"security_group":{"name":"web","description":"web","stateful":false,"project_id":"`+sgTestProjectID+`"}}`)
		return `{"security_group":{"id":"sg-1","name":"web","tags":[]}}`
	})
	var tagged bool
	fakeServer.Mux.HandleFunc("/v2.0/security-groups/sg-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"tags":["t1"]}`)
		tagged = true
		writeJSON(t, w, http.StatusOK, `{"tags":["t1"]}`)
	})
	out, err := execNetwork(t, fakeServer, "security", "group", "create", "web",
		"--stateless", "--project", sgTestProjectID, "--tag", "t1")
	if err != nil {
		t.Fatalf("security group create: %v (%s)", err, out)
	}
	if !tagged {
		t.Error("--tag did not reach the tags PUT")
	}
}

func TestExec_SecurityGroupUnset_AllTag(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	emptyLookup(t, fakeServer, "/v2.0/security-groups", "security_groups")
	fakeServer.Mux.HandleFunc("/v2.0/security-groups/sg-1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"security_group":{"id":"sg-1","tags":["a"]}}`)
	})
	fakeServer.Mux.HandleFunc("/v2.0/security-groups/sg-1/tags", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"tags":[]}`)
		writeJSON(t, w, http.StatusOK, `{"tags":[]}`)
	})
	if out, err := execNetwork(t, fakeServer, "security", "group", "unset", "sg-1", "--all-tag"); err != nil {
		t.Fatalf("security group unset --all-tag: %v (%s)", err, out)
	}
	if _, err := execNetwork(t, fakeServer, "security", "group", "unset", "sg-1", "--all-tag", "--tag", "a"); err == nil {
		t.Error("--tag and --all-tag together were accepted")
	}
}

func TestExec_SecurityGroupRuleList_FiltersReachTheQuery(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var got map[string][]string
	fakeServer.Mux.HandleFunc("/v2.0/security-group-rules", func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		writeJSON(t, w, http.StatusOK, `{"security_group_rules":[]}`)
	})
	out, err := execNetwork(t, fakeServer, "security", "group", "rule", "list",
		"--ingress", "--protocol", "TCP", "--ethertype", "IPv4", "--project", sgTestProjectID, "--long")
	if err != nil {
		t.Fatalf("security group rule list: %v (%s)", err, out)
	}
	want := map[string][]string{
		"direction": {"ingress"}, "protocol": {"tcp"}, "ethertype": {"IPv4"}, "project_id": {sgTestProjectID},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("query = %v, want %v", got, want)
	}
}
