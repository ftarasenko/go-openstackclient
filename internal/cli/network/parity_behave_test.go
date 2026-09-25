package network

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Behaviour parity (network-parity.md §6e items 2–7): same command, same flags,
// now the same result as upstream python-openstackclient 10.3.0.

// failOnRequest registers a handler that fails the test if path is requested at
// all — for errors that must be raised client-side, before any request.
func failOnRequest(t *testing.T, fakeServer th.FakeServer, path string) {
	t.Helper()
	fakeServer.Mux.HandleFunc(path, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected %s %s: the error must be raised before any request", r.Method, r.URL.Path)
	})
}

// wantErr fails unless err is non-nil and contains want.
func wantErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want one containing %q", err, want)
	}
}

// --- 2. ip availability list --ip-version -----------------------------------

// Upstream's choices are 4 and 6; anything else is refused before the list.
func TestExec_IPAvailabilityList_RejectsAnIPVersionOutsideTheChoices(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	failOnRequest(t, fakeServer, "/v2.0/network-ip-availabilities")
	_, err := execNetwork(t, fakeServer, "ip", "availability", "list", "--ip-version", "5")
	wantErr(t, err, "--ip-version must be 4 or 6")
}

// --- 3. port unset: exact entries, error when absent --------------------------

const behavePortBody = `{"port":{"id":"port-1","revision_number":7,
  "fixed_ips":[{"subnet_id":"sub-1","ip_address":"10.0.0.5"},
               {"subnet_id":"sub-1","ip_address":"10.0.0.6"},
               {"subnet_id":"sub-2","ip_address":"10.0.1.5"}],
  "security_groups":["sg-1","sg-2"],
  "allowed_address_pairs":[{"ip_address":"10.0.0.50","mac_address":"fa:16:3e:00:00:01"},
                           {"ip_address":"10.0.0.51","mac_address":"fa:16:3e:00:00:02"}]}}`

// behavePortServer serves port-1 (GET) and hands every PUT body to put; with
// put nil a PUT fails the test.
func behavePortServer(t *testing.T, fakeServer th.FakeServer, put func(r *http.Request)) {
	t.Helper()
	echoLookup(t, fakeServer, "/ports", "ports")
	echoLookup(t, fakeServer, "/subnets", "subnets")
	echoLookup(t, fakeServer, "/security-groups", "security_groups")
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, behavePortBody)
			return
		}
		if put == nil {
			t.Errorf("unexpected %s: the unset must fail before the update", r.Method)
			return
		}
		th.TestMethod(t, r, http.MethodPut)
		put(r)
		writeJSON(t, w, http.StatusOK, `{"port":{"id":"port-1"}}`)
	})
}

func TestRunPortUnset_RemovesExactlyTheNamedEntries(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	behavePortServer(t, fakeServer, func(r *http.Request) {
		// One entry per spec: the full fixed-ip spec, the address-only spec
		// (unique on the port) and the security group by name; the pair is
		// named with its MAC.
		th.TestJSONRequest(t, r, `{"port":{
		  "fixed_ips":[{"subnet_id":"sub-1","ip_address":"10.0.0.6"}],
		  "security_groups":["sg-1"],
		  "allowed_address_pairs":[{"ip_address":"10.0.0.51","mac_address":"fa:16:3e:00:00:02"}]}}`)
	})
	f := &portUnsetFlags{
		fixedIP:        []string{"subnet=sub-1,ip-address=10.0.0.5", "ip-address=10.0.1.5"},
		securityGroup:  []string{"sg-2"},
		allowedAddress: []string{"ip-address=10.0.0.50,mac-address=fa:16:3e:00:00:01"},
	}
	if err := runPortUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
		"port-1", f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runPortUnset: %v", err)
	}
}

// Upstream: "Port does not contain fixed-ip …" / "… security group …" /
// "… allowed-address-pair …", raised before the update; koc also refuses a
// partial spec that matches more than one entry instead of removing all of them.
func TestRunPortUnset_ErrorsOnAnEntryThePortDoesNotCarry(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    portUnsetFlags
		want string
	}{
		{"fixed ip address", portUnsetFlags{fixedIP: []string{"ip-address=10.0.0.9"}},
			"port does not contain fixed-ip ip-address=10.0.0.9"},
		{"fixed ip on the wrong subnet", portUnsetFlags{fixedIP: []string{"subnet=sub-2,ip-address=10.0.0.5"}},
			"port does not contain fixed-ip subnet=sub-2,ip-address=10.0.0.5"},
		{"the same fixed ip twice", portUnsetFlags{fixedIP: []string{"ip-address=10.0.1.5", "ip-address=10.0.1.5"}},
			"port does not contain fixed-ip ip-address=10.0.1.5"},
		{"ambiguous subnet-only spec", portUnsetFlags{fixedIP: []string{"subnet=sub-1"}},
			"fixed-ip subnet=sub-1 matches 2 entries of the port"},
		{"security group", portUnsetFlags{securityGroup: []string{"sg-9"}},
			"port does not contain security group sg-9"},
		{"allowed address pair with another MAC", portUnsetFlags{
			allowedAddress: []string{"ip-address=10.0.0.50,mac-address=fa:16:3e:00:00:99"}},
			"port does not contain allowed-address-pair ip-address=10.0.0.50,mac-address=fa:16:3e:00:00:99"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			behavePortServer(t, fakeServer, nil)
			err := runPortUnset(context.Background(), networkClient(fakeServer), &output.Options{Format: output.FormatValue},
				"port-1", &tc.f, &bytes.Buffer{})
			wantErr(t, err, tc.want)
		})
	}
}

// --- 4. network qos rule create/set: per-type parameters -----------------------

func TestExec_QoSRuleCreate_ChecksTheTypeParameters(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"flag of another type", []string{"--type", "dscp-marking", "--dscp-mark", "26", "--max-kbps", "1000"},
			`rule type "dscp-marking" only accepts arguments: --dscp-mark (got --max-kbps)`},
		{"direction on dscp-marking", []string{"--type", "dscp-marking", "--dscp-mark", "26", "--egress"},
			`rule type "dscp-marking" only accepts arguments: --dscp-mark (got --egress)`},
		{"burst outside bandwidth-limit", []string{"--type", "minimum-bandwidth", "--min-kbps", "10",
			"--egress", "--max-burst-kbits", "5"},
			`rule type "minimum-bandwidth" only accepts arguments: --ingress/--egress, --min-kbps (got --max-burst-kbits)`},
		{"missing direction", []string{"--type", "minimum-bandwidth", "--min-kbps", "10"},
			`"create" rule command for type "minimum-bandwidth" requires arguments: --ingress/--egress`},
		{"missing everything", []string{"--type", "minimum-packet-rate"},
			`"create" rule command for type "minimum-packet-rate" requires arguments: --ingress/--egress/--any, --min-kpps`},
		{"missing max-kbps", []string{"--type", "bandwidth-limit", "--egress"},
			`"create" rule command for type "bandwidth-limit" requires arguments: --max-kbps`},
		{"missing dscp-mark", []string{"--type", "dscp-marking"},
			`"create" rule command for type "dscp-marking" requires arguments: --dscp-mark`},
		{"--any outside minimum-packet-rate", []string{"--type", "minimum-bandwidth", "--min-kbps", "10", "--any"},
			"--any can only be used with a minimum-packet-rate rule"},
		{"--direction any outside minimum-packet-rate", []string{"--type", "bandwidth-limit", "--max-kbps", "10",
			"--direction", "any"},
			"--direction any can only be used with a minimum-packet-rate rule"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			failOnRequest(t, fakeServer, "/")
			argv := append([]string{"network", "qos", "rule", "create", "gold"}, tc.argv...)
			_, err := execNetwork(t, fakeServer, argv...)
			wantErr(t, err, tc.want)
		})
	}
}

// A complete set of parameters passes, and koc's --direction still counts as
// the direction parameter.
func TestExec_QoSRuleCreate_AcceptsTheTypeParametersIncludingDirectionAlias(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/v2.0/qos/policies", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"policies":[{"id":"`+qosPolicyID+`","name":"gold"}]}`)
	})
	fakeServer.Mux.HandleFunc("/v2.0/qos/policies/"+qosPolicyID+"/minimum_bandwidth_rules",
		func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodPost)
			th.TestJSONRequest(t, r, `{"minimum_bandwidth_rule":{"min_kbps":10,"direction":"egress"}}`)
			writeJSON(t, w, http.StatusCreated, `{"minimum_bandwidth_rule":{"id":"`+qosRuleID+`"}}`)
		})
	out, err := execNetwork(t, fakeServer, "network", "qos", "rule", "create", "gold",
		"--type", "minimum-bandwidth", "--min-kbps", "10", "--direction", "egress")
	if err != nil {
		t.Fatalf("qos rule create: %v (%s)", err, out)
	}
	if !strings.Contains(out, qosRuleID) {
		t.Errorf("output missing the created rule:\n%s", out)
	}
}

// On set the type comes from the policy (a bandwidth-limit rule here); a flag
// of another type is refused before the PUT, and nothing is required.
func TestRunQoSRuleSet_RefusesAFlagOfAnotherType(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    qosRuleFlags
		want string
	}{
		{"dscp mark", qosRuleFlags{dscpMark: 26, changed: changedFlags{"dscp-mark": true}},
			`rule type "bandwidth-limit" only accepts arguments: --ingress/--egress, --max-burst-kbits, --max-kbps (got --dscp-mark)`},
		{"min kbps", qosRuleFlags{minKBps: 5, changed: changedFlags{"min-kbps": true}},
			`rule type "bandwidth-limit" only accepts arguments: --ingress/--egress, --max-burst-kbits, --max-kbps (got --min-kbps)`},
		{"any", qosRuleFlags{anyDirection: true, changed: changedFlags{flagQoSAny: true}},
			"--any can only be used with a minimum-packet-rate rule, not bandwidth-limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			handleQoSPolicyLookup(t, fakeServer)
			failOnRequest(t, fakeServer, "/qos/policies/"+qosPolicyID+"/bandwidth_limit_rules/"+qosRuleID)
			err := runQoSRuleSet(context.Background(), networkClient(fakeServer), &output.Options{Format: "value"},
				"gold", qosRuleID, &tc.f, &bytes.Buffer{})
			wantErr(t, err, tc.want)
		})
	}
}

func TestRunQoSRuleSet_SendsAnOptionalParameterAlone(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	handleQoSPolicyLookup(t, fakeServer)
	fakeServer.Mux.HandleFunc("/qos/policies/"+qosPolicyID+"/bandwidth_limit_rules/"+qosRuleID,
		func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodPut)
			th.TestJSONRequest(t, r, `{"bandwidth_limit_rule":{"max_burst_kbps":300,"direction":"ingress"}}`)
			writeJSON(t, w, http.StatusOK, `{"bandwidth_limit_rule":{"id":"`+qosRuleID+`"}}`)
		})
	f := &qosRuleFlags{maxBurstKbits: 300, ingress: true,
		changed: changedFlags{"max-burst-kbits": true, flagQoSIngress: true}}
	if err := runQoSRuleSet(context.Background(), networkClient(fakeServer), &output.Options{Format: "value"},
		"gold", qosRuleID, f, &bytes.Buffer{}); err != nil {
		t.Fatalf("runQoSRuleSet: %v", err)
	}
}

// --- 5. network rbac create <object> by name, per --type ----------------------

func TestRunRBACCreate_ResolvesTheObjectByNamePerType(t *testing.T) {
	for _, tc := range []struct {
		objectType, path, key string
	}{
		{"network", "/networks", "networks"},
		{"qos_policy", "/qos/policies", "policies"},
		{"security_group", "/security-groups", "security_groups"},
		{"address_scope", "/address-scopes", "address_scopes"},
		{"subnetpool", "/subnetpools", "subnetpools"},
		{"address_group", "/address-groups", "address_groups"},
	} {
		t.Run(tc.objectType, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			// echoLookup answers the name lookup with ID == name, so the object
			// ID in the POST proves the lookup ran against the type's collection.
			echoLookup(t, fakeServer, tc.path, tc.key)
			fakeServer.Mux.HandleFunc("/rbac-policies", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodPost)
				th.TestJSONRequest(t, r, `{"rbac_policy":{"action":"access_as_shared","object_type":"`+tc.objectType+`",
				  "object_id":"obj-by-name","target_tenant":"*"}}`)
				writeJSON(t, w, http.StatusCreated, `{"rbac_policy":{"id":"`+extRBACID+`","object_id":"obj-by-name"}}`)
			})
			f := &rbacCreateFlags{action: "access_as_shared", objectType: tc.objectType, targetProject: rbacAllProjects}
			var buf bytes.Buffer
			if err := runRBACCreate(context.Background(), networkClient(fakeServer), &output.Options{Format: "value"},
				"obj-by-name", f, &buf); err != nil {
				t.Fatalf("runRBACCreate: %v", err)
			}
			if !strings.Contains(buf.String(), extRBACID) {
				t.Errorf("output missing the policy:\n%s", buf.String())
			}
		})
	}
}

// Upstream: `openstack network rbac create … --type <choice>`; the lookup of a
// missing name errors rather than sending the literal reference.
func TestRBACCreate_RejectsAnUnknownTypeAndAMissingObject(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	failOnRequest(t, fakeServer, "/")
	_, err := execNetwork(t, fakeServer, "network", "rbac", "create", "n1",
		"--type", "router", "--action", "access_as_shared", "--target-all-projects")
	wantErr(t, err, `invalid --type "router": choose from address_group, address_scope, security_group, subnetpool, qos_policy, network`)

	fs2 := th.SetupHTTP()
	defer fs2.Teardown()
	fs2.Mux.HandleFunc("/networks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"networks":[]}`)
	})
	failOnRequest(t, fs2, "/rbac-policies")
	err = runRBACCreate(context.Background(), networkClient(fs2), &output.Options{Format: "value"}, "typo",
		&rbacCreateFlags{action: "access_as_shared", objectType: "network", targetProject: rbacAllProjects}, &bytes.Buffer{})
	wantErr(t, err, `no network found for "typo"`)
}

// --- 6. floating ip port forwarding … <floating-ip> by address -----------------

const behaveFIPAddr = "203.0.113.10"

// behavePFServer resolves behaveFIPAddr (echoLookup: the ID is the address
// itself) and serves the forwarding endpoints under it.
func behavePFServer(t *testing.T, fakeServer th.FakeServer, handle func(w http.ResponseWriter, r *http.Request)) {
	t.Helper()
	echoLookup(t, fakeServer, "/floatingips", "floatingips")
	fakeServer.Mux.HandleFunc("/floatingips/"+behaveFIPAddr+"/port_forwardings", handle)
	fakeServer.Mux.HandleFunc("/floatingips/"+behaveFIPAddr+"/port_forwardings/"+extPFID, handle)
}

func TestPortForwardingVerbs_AcceptAFloatingIPAddress(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var calls []string
	behavePFServer(t, fakeServer, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			if strings.HasSuffix(r.URL.Path, "/port_forwardings") {
				writeJSON(t, w, http.StatusOK, `{"port_forwardings":[{"id":"`+extPFID+`"}]}`)
				return
			}
			writeJSON(t, w, http.StatusOK, `{"port_forwarding":{"id":"`+extPFID+`"}}`)
		case http.MethodPost:
			writeJSON(t, w, http.StatusCreated, `{"port_forwarding":{"id":"`+extPFID+`"}}`)
		default:
			writeJSON(t, w, http.StatusOK, `{"port_forwarding":{"id":"`+extPFID+`"}}`)
		}
	})
	ctx, c, o := context.Background(), networkClient(fakeServer), &output.Options{Format: "value"}
	var buf bytes.Buffer
	if err := runPortForwardingList(ctx, c, o, behaveFIPAddr, &portForwardingListFlags{}, &buf); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := runPortForwardingShow(ctx, c, o, behaveFIPAddr, extPFID, &buf); err != nil {
		t.Fatalf("show: %v", err)
	}
	if err := runPortForwardingCreate(ctx, c, o, behaveFIPAddr, &portForwardingFlags{port: extPortID,
		internalIP: "192.0.2.10", internalPortArg: "22", externalPortArg: "2222", protocol: "tcp"}, &buf); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := runPortForwardingSet(ctx, c, o, behaveFIPAddr, extPFID, &portForwardingFlags{protocol: "udp"}, &buf); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := runPortForwardingDelete(ctx, c, behaveFIPAddr, []string{extPFID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	base := "/floatingips/" + behaveFIPAddr + "/port_forwardings"
	want := []string{"GET " + base, "GET " + base + "/" + extPFID, "POST " + base,
		"PUT " + base + "/" + extPFID, "DELETE " + base + "/" + extPFID}
	th.AssertDeepEquals(t, want, calls)
}

func TestRunPortForwardingDelete_ErrorsOnAnUnknownFloatingIPAddress(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/floatingips", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, behaveFIPAddr, r.URL.Query().Get("floating_ip_address"))
		writeJSON(t, w, http.StatusOK, `{"floatingips":[]}`)
	})
	err := runPortForwardingDelete(context.Background(), networkClient(fakeServer), behaveFIPAddr, []string{extPFID})
	wantErr(t, err, `no floating IP found for "`+behaveFIPAddr+`"`)
}

// --- 7. --internal/--external-protocol-port N or N:M ------------------------------

func TestRunPortForwardingCreate_PortSpellings(t *testing.T) {
	for _, tc := range []struct {
		name               string
		internal, external string
		intRange, extRange string // the koc-only --*-range aliases
		body               string
	}{
		{name: "single ports", internal: "22", external: "2222",
			body: `"internal_port":22,"external_port":2222`},
		{name: "ranges", internal: "22:23", external: "2222:2223",
			body: `"internal_port_range":"22:23","external_port_range":"2222:2223"`},
		{name: "1:N", internal: "22", external: "2222:2223",
			body: `"internal_port":22,"external_port_range":"2222:2223"`},
		{name: "range aliases", intRange: "22:23", extRange: "2222:2223",
			body: `"internal_port_range":"22:23","external_port_range":"2222:2223"`},
		{name: "upstream internal with alias external", internal: "22:23", extRange: "2222:2223",
			body: `"internal_port_range":"22:23","external_port_range":"2222:2223"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			fakeServer.Mux.HandleFunc("/floatingips/"+extFIPID+"/port_forwardings", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodPost)
				th.TestJSONRequest(t, r, `{"port_forwarding":{"protocol":"tcp","internal_port_id":"`+extPortID+`",
				  "internal_ip_address":"192.0.2.10",`+tc.body+`}}`)
				writeJSON(t, w, http.StatusCreated, `{"port_forwarding":{"id":"`+extPFID+`"}}`)
			})
			f := &portForwardingFlags{port: extPortID, internalIP: "192.0.2.10", protocol: "tcp",
				internalPortArg: tc.internal, externalPortArg: tc.external,
				internalPortRange: tc.intRange, externalPortRange: tc.extRange}
			if err := runPortForwardingCreate(context.Background(), networkClient(fakeServer),
				&output.Options{Format: "value"}, extFIPID, f, &bytes.Buffer{}); err != nil {
				t.Fatalf("runPortForwardingCreate: %v", err)
			}
		})
	}
}

// Upstream's validate_and_assign_port_ranges messages, raised before any
// request (the floating IP is a UUID, so no lookup either).
func TestRunPortForwardingCreateAndSet_ValidateThePorts(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    portForwardingFlags
		want string
	}{
		{"N:M internal to a single external", portForwardingFlags{internalPortArg: "22:23", externalPortArg: "2222"},
			"the relation between internal and external ports does not match the pattern 1:N and N:N"},
		{"ranges of different widths", portForwardingFlags{internalPortArg: "22:25", externalPortArg: "80:81"},
			"the relation between internal and external ports does not match the pattern 1:N and N:N"},
		{"descending range", portForwardingFlags{internalPortArg: "23:22", externalPortArg: "81:80"},
			"the last number in port range must be greater or equal to the first"},
		{"descending alias range", portForwardingFlags{externalPortRange: "81:80"},
			"the last number in port range must be greater or equal to the first"},
		{"port zero", portForwardingFlags{internalPortArg: "0", externalPortArg: "80"},
			"the port number range is <1-65535>"},
		{"port above 65535", portForwardingFlags{internalPortArg: "22", externalPortArg: "65536"},
			"the port number range is <1-65535>"},
		{"not a number", portForwardingFlags{internalPortArg: "ssh"},
			`--internal-protocol-port "ssh" is not a port number or <first>:<last> range`},
		{"three-part range", portForwardingFlags{externalPortArg: "1:2:3"},
			`--external-protocol-port "1:2:3" is not a port number or <first>:<last> range`},
		{"both spellings of one side", portForwardingFlags{internalPortArg: "22:23", internalPortRange: "22:23"},
			"--internal-protocol-port and --internal-protocol-port-range are mutually exclusive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			failOnRequest(t, fakeServer, "/")
			ctx, c, o := context.Background(), networkClient(fakeServer), &output.Options{Format: "value"}
			create, set := tc.f, tc.f
			create.port, create.internalIP, create.protocol = extPortID, "192.0.2.10", "tcp"
			wantErr(t, runPortForwardingCreate(ctx, c, o, extFIPID, &create, &bytes.Buffer{}), tc.want)
			wantErr(t, runPortForwardingSet(ctx, c, o, extFIPID, extPFID, &set, &bytes.Buffer{}), tc.want)
		})
	}
}

// Through cobra: the upstream flag takes N:M on set, the koc alias still
// works, and the two spellings of one side are exclusive.
func TestExec_PortForwardingSet_PortFlags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var bodies []any
	fakeServer.Mux.HandleFunc("/v2.0/floatingips/"+extFIPID+"/port_forwardings/"+extPFID,
		func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodPut)
			var body any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
			bodies = append(bodies, body)
			writeJSON(t, w, http.StatusOK, `{"port_forwarding":{"id":"`+extPFID+`"}}`)
		})
	set := []string{"floating", "ip", "port", "forwarding", "set", extFIPID, extPFID}
	if out, err := execNetwork(t, fakeServer, append(set, "--internal-protocol-port", "22:23",
		"--external-protocol-port", "2222:2223")...); err != nil {
		t.Fatalf("set with ranges: %v (%s)", err, out)
	}
	if out, err := execNetwork(t, fakeServer, append(set, "--external-protocol-port", "8080")...); err != nil {
		t.Fatalf("set with a single port: %v (%s)", err, out)
	}
	if out, err := execNetwork(t, fakeServer, append(set, "--internal-protocol-port-range", "22:23",
		"--external-protocol-port-range", "2222:2223")...); err != nil {
		t.Fatalf("set with the range aliases: %v (%s)", err, out)
	}
	ranges := map[string]any{"port_forwarding": map[string]any{
		"internal_port_range": "22:23", "external_port_range": "2222:2223"}}
	want := []any{ranges, map[string]any{"port_forwarding": map[string]any{"external_port": float64(8080)}}, ranges}
	th.AssertDeepEquals(t, want, bodies)

	_, err := execNetwork(t, fakeServer, append(set, "--external-protocol-port", "80",
		"--external-protocol-port-range", "80:81")...)
	wantErr(t, err, "none of the others can be")
	th.AssertEquals(t, 3, len(bodies))
}
