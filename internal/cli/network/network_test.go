package network

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// changedFlags is a minimal flagSet stand-in for the set/unset seams: a flag
// name is "changed" when present with a true value.
type changedFlags map[string]bool

func (c changedFlags) Changed(name string) bool { return c[name] }

// networkClient returns a service client wired to the mock server with the
// neutron service type. The network service uses no microversion header, so
// sc.Microversion is left empty, mirroring auth.Client.Network.
func networkClient(fakeServer th.FakeServer) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "network"
	return sc
}

const networkListBody = `{
  "networks": [
    {
      "id": "11111111-1111-1111-1111-111111111111",
      "name": "public",
      "status": "ACTIVE",
      "admin_state_up": true,
      "shared": true,
      "router:external": true,
      "mtu": 1500,
      "subnets": ["aaaa"],
      "provider:network_type": "flat"
    },
    {
      "id": "22222222-2222-2222-2222-222222222222",
      "name": "private",
      "status": "ACTIVE",
      "admin_state_up": true,
      "shared": false,
      "router:external": false,
      "mtu": 1442,
      "subnets": ["bbbb"],
      "provider:network_type": "vxlan"
    }
  ]
}`

func TestRunNetworkList_RequestAndTableOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(networkListBody))
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runNetworkList(context.Background(), client, o, &networkListFlags{}, &buf); err != nil {
		t.Fatalf("runNetworkList returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("request method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{"ID", "Name", "Subnets", "public", "private", "11111111-1111-1111-1111-111111111111"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n---\n%s", want, out)
		}
	}
	// --long-only columns should not appear by default.
	if strings.Contains(out, "Network Type") {
		t.Errorf("default output should not contain --long columns:\n%s", out)
	}
}

func TestRunNetworkList_LongIncludesExtensionColumns(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(networkListBody))
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}

	var buf bytes.Buffer
	if err := runNetworkList(context.Background(), client, o, &networkListFlags{long: true}, &buf); err != nil {
		t.Fatalf("runNetworkList returned error: %v", err)
	}
	out := buf.String()
	// mtu and provider network type from the extension embeds must render.
	for _, want := range []string{"Network Type", "MTU", "vxlan", "1442"} {
		if !strings.Contains(out, want) {
			t.Errorf("--long output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunNetworkCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{
			"network": {
				"name": "netA",
				"admin_state_up": true,
				"mtu": 1442
			}
		}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"network":{"id":"nnnn","name":"netA","admin_state_up":true,"mtu":1442}}`))
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &networkCreateFlags{mtu: 1442}

	var buf bytes.Buffer
	if err := runNetworkCreate(context.Background(), client, o, "netA", f, &buf); err != nil {
		t.Fatalf("runNetworkCreate returned error: %v", err)
	}
}

func TestRunSubnetCreate_RequestBodyHasNetworkID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// resolveNetworkID lists networks filtered by name; return no match so the
	// argument is treated as an ID and passed straight through.
	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"networks":[]}`))
	})
	fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{
			"subnet": {
				"name": "subA",
				"network_id": "net-id-123",
				"cidr": "10.0.0.0/24",
				"ip_version": 4
			}
		}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"subnet":{"id":"ssss","name":"subA","network_id":"net-id-123","cidr":"10.0.0.0/24","ip_version":4}}`))
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &subnetCreateFlags{network: "net-id-123", subnetRange: "10.0.0.0/24", ipVersion: 4}

	var buf bytes.Buffer
	if err := runSubnetCreate(context.Background(), client, o, "subA", f, &buf); err != nil {
		t.Fatalf("runSubnetCreate returned error: %v", err)
	}
}

func TestRunAgentList_AgentTypeFilterQueryParam(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/agents", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"agent_type": "l3"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"agents":[{"id":"agent-1","agent_type":"L3 agent","host":"cmp1","admin_state_up":true,"alive":true,"binary":"neutron-l3-agent"}]}`))
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatTable}
	f := &agentListFlags{agentType: "l3"}

	var buf bytes.Buffer
	if err := runAgentList(context.Background(), client, o, f, &buf); err != nil {
		t.Fatalf("runAgentList returned error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"agent-1", "L3 agent", "cmp1"} {
		if !strings.Contains(out, want) {
			t.Errorf("agent list output missing %q\n---\n%s", want, out)
		}
	}
}

// TestRunSecurityGroupRuleCreate_NormalizesEtherTypeAndProtocol confirms that
// mixed-case --ethertype and --protocol values are canonicalized before the
// request body is built ("ipv6" -> "IPv6", "TCP" -> "tcp").
func TestRunSecurityGroupRuleCreate_NormalizesEtherTypeAndProtocol(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// resolveSecGroupID lists groups by name; return no match so the arg is
	// treated as an ID and passed straight through.
	fakeServer.Mux.HandleFunc("/security-groups", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"security_groups":[]}`))
	})
	fakeServer.Mux.HandleFunc("/security-group-rules", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{
			"security_group_rule": {
				"direction": "ingress",
				"ethertype": "IPv6",
				"protocol": "tcp",
				"security_group_id": "sg-id-1"
			}
		}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"security_group_rule":{"id":"rule-1","security_group_id":"sg-id-1","direction":"ingress","ethertype":"IPv6","protocol":"tcp"}}`))
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &secGroupRuleCreateFlags{protocol: "TCP", ethertype: "ipv6"}

	var buf bytes.Buffer
	if err := runSecurityGroupRuleCreate(context.Background(), client, o, "sg-id-1", f, &buf); err != nil {
		t.Fatalf("runSecurityGroupRuleCreate returned error: %v", err)
	}
}

// TestRunSecurityGroupRuleCreate_InfersIPv6FromRemoteIP confirms that when
// --ethertype is not given and --remote-ip is an IPv6 CIDR the ethertype is
// inferred as IPv6 rather than defaulting to IPv4.
func TestRunSecurityGroupRuleCreate_InfersIPv6FromRemoteIP(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/security-groups", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"security_groups":[]}`))
	})
	fakeServer.Mux.HandleFunc("/security-group-rules", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{
			"security_group_rule": {
				"direction": "ingress",
				"ethertype": "IPv6",
				"security_group_id": "sg-id-1",
				"remote_ip_prefix": "2001:db8::/64"
			}
		}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"security_group_rule":{"id":"rule-1","security_group_id":"sg-id-1","direction":"ingress","ethertype":"IPv6"}}`))
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &secGroupRuleCreateFlags{remoteIP: "2001:db8::/64"}

	var buf bytes.Buffer
	if err := runSecurityGroupRuleCreate(context.Background(), client, o, "sg-id-1", f, &buf); err != nil {
		t.Fatalf("runSecurityGroupRuleCreate returned error: %v", err)
	}
}

// TestRunFloatingIPCreate_AssociatesPortAtCreation confirms --port resolves to
// a port_id and --fixed-ip-address flows into the create body.
func TestRunFloatingIPCreate_AssociatesPortAtCreation(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"networks":[]}`))
	})
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ports":[]}`))
	})
	fakeServer.Mux.HandleFunc("/floatingips", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{
			"floatingip": {
				"floating_network_id": "ext-net-1",
				"port_id": "port-1",
				"fixed_ip_address": "10.0.0.5"
			}
		}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"floatingip":{"id":"fip-1","floating_network_id":"ext-net-1","port_id":"port-1","fixed_ip_address":"10.0.0.5"}}`))
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &floatingIPCreateFlags{port: "port-1", fixedIPAddr: "10.0.0.5"}

	var buf bytes.Buffer
	if err := runFloatingIPCreate(context.Background(), client, o, "ext-net-1", f, &buf); err != nil {
		t.Fatalf("runFloatingIPCreate returned error: %v", err)
	}
}

// TestRunRouterAddPort confirms "router add port" resolves the router and port
// and issues a PUT add_router_interface with the port_id body.
func TestRunRouterAddPort(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/routers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"routers":[]}`))
	})
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ports":[]}`))
	})
	fakeServer.Mux.HandleFunc("/routers/router-1/add_router_interface", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestJSONRequest(t, r, `{"port_id": "port-1"}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"router-1","port_id":"port-1","subnet_id":"subnet-1"}`))
	})

	client := networkClient(fakeServer)

	var buf bytes.Buffer
	if err := runRouterAddPort(context.Background(), client, "router-1", "port-1", &buf); err != nil {
		t.Fatalf("runRouterAddPort returned error: %v", err)
	}
	if !strings.Contains(buf.String(), "Added interface for port port-1 to router router-1") {
		t.Errorf("unexpected output: %q", buf.String())
	}
}

// TestRunPortCreate_SecurityGroupsAndAdminState confirms --security-group and
// --disable flow into the create body.
func TestRunPortCreate_SecurityGroupsAndAdminState(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"networks":[]}`))
	})
	fakeServer.Mux.HandleFunc("/security-groups", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"security_groups":[]}`))
	})
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{
			"port": {
				"network_id": "net-1",
				"name": "portA",
				"admin_state_up": false,
				"security_groups": ["sg-1", "sg-2"]
			}
		}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"port":{"id":"port-1","name":"portA","network_id":"net-1","admin_state_up":false,"security_groups":["sg-1","sg-2"]}}`))
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &portCreateFlags{network: "net-1", securityGroup: []string{"sg-1", "sg-2"}, disable: true}

	var buf bytes.Buffer
	if err := runPortCreate(context.Background(), client, o, "portA", f, &buf); err != nil {
		t.Fatalf("runPortCreate returned error: %v", err)
	}
}

// TestRunSubnetSet_NoDHCPAndNoGateway confirms --no-dhcp sends enable_dhcp:false
// and --no-gateway clears the gateway via an empty-string pointer.
func TestRunSubnetSet_NoDHCPAndNoGateway(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"subnets":[]}`))
	})
	fakeServer.Mux.HandleFunc("/subnets/subnet-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		// A *string pointing to "" serializes to null via gophercloud's
		// omitempty handling, which neutron treats as clearing the gateway.
		th.TestJSONRequest(t, r, `{
			"subnet": {
				"enable_dhcp": false,
				"gateway_ip": null
			}
		}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"subnet":{"id":"subnet-1","enable_dhcp":false,"gateway_ip":null}}`))
	})

	client := networkClient(fakeServer)
	o := &output.Options{Format: output.FormatValue}
	f := &subnetSetFlags{noDHCP: true, noGateway: true}

	var buf bytes.Buffer
	if err := runSubnetSet(context.Background(), client, o, "subnet-1", f, changedFlags{"no-dhcp": true, "no-gateway": true}, &buf); err != nil {
		t.Fatalf("runSubnetSet returned error: %v", err)
	}
}
