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
