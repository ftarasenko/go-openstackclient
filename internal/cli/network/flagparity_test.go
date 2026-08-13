package network

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/ports"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "subnet list" took no filters at all before this pass, so the query string is
// the whole point of the test.
func TestRunSubnetList_SendsEveryFilter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// resolveNetworkID / resolveSubnetPoolID name-filter first; empty results fall
	// back to the literal reference.
	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"networks": []}`))
	})
	fakeServer.Mux.HandleFunc("/subnetpools", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subnetpools": []}`))
	})
	fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{
			"name":          "private",
			"network_id":    "net-1",
			"project_id":    "p1",
			"subnetpool_id": "sp-1",
			"gateway_ip":    "10.0.0.1",
			"ip_version":    "4",
			"enable_dhcp":   "true",
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subnets": [{
          "id": "sub-1", "name": "private", "network_id": "net-1", "cidr": "10.0.0.0/24",
          "ip_version": 4, "gateway_ip": "10.0.0.1", "enable_dhcp": true,
          "project_id": "p1", "subnetpool_id": "sp-1",
          "allocation_pools": [{"start": "10.0.0.2", "end": "10.0.0.254"}]
        }]}`))
	})

	dhcp := true
	f := &subnetListFlags{
		name:       "private",
		network:    "net-1",
		subnetPool: "sp-1",
		gateway:    "10.0.0.1",
		ipVersion:  4,
		enableDHCP: &dhcp,
		long:       true,
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runSubnetList(context.Background(), networkClient(fakeServer), o, f, "p1", &buf); err != nil {
		t.Fatalf("runSubnetList error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"sub-1", "private", "10.0.0.0/24",
		// --long columns, including the allocation pool rendered as start-end.
		"Gateway", "Subnet Pool", "Allocation Pools", "10.0.0.2-10.0.0.254",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("subnet list output missing %q\n---\n%s", want, out)
		}
	}
}

// The provider-extension filters have no gophercloud ListOpts fields, so they are
// added by a local ListOptsExt — and must compose with --external rather than
// replacing it.
func TestRunNetworkList_ProviderFiltersComposeWithExternal(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/networks", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{
			"name":                      "provider-net",
			"status":                    "ACTIVE",
			"shared":                    "true",
			"project_id":                "p1",
			"router:external":           "true",
			"provider:network_type":     "vlan",
			"provider:physical_network": "physnet1",
			"provider:segmentation_id":  "1234",
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"networks": [{
          "id": "net-1", "name": "provider-net", "status": "ACTIVE", "shared": true,
          "admin_state_up": true, "subnets": ["sub-1"], "project_id": "p1",
          "provider:network_type": "vlan", "provider:physical_network": "physnet1",
          "provider:segmentation_id": "1234", "router:external": true, "mtu": 1500
        }]}`))
	})

	shared := true
	f := &networkListFlags{
		name:                    "provider-net",
		status:                  "ACTIVE",
		shared:                  &shared,
		external:                true,
		externalSet:             true,
		providerNetworkType:     "vlan",
		providerPhysicalNetwork: "physnet1",
		providerSegment:         "1234",
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runNetworkList(context.Background(), networkClient(fakeServer), o, f, "p1", &buf); err != nil {
		t.Fatalf("runNetworkList error: %v", err)
	}
	if !strings.Contains(buf.String(), "provider-net") {
		t.Errorf("network list output missing the network\n---\n%s", buf.String())
	}
}

func TestRunPortSet_AllowedAddressAndBindingAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports": []}`))
	})
	var gotMethod string
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		// port_security_enabled and binding:host_id come from the local
		// UpdateOptsBuilder extension, not ports.UpdateOpts.
		th.TestJSONRequest(t, r, `{"port": {
          "allowed_address_pairs": [
            {"ip_address": "10.0.0.50"},
            {"ip_address": "10.0.0.51", "mac_address": "fa:16:3e:aa:bb:cc"}
          ],
          "device_id": "dev-9",
          "device_owner": "compute:nova",
          "binding:host_id": "cmp1",
          "port_security_enabled": true
        }}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"port": {"id": "port-1", "name": "p", "network_id": "net-1"}}`))
	})

	f := &portSetFlags{
		allowedAddress:     []string{"ip-address=10.0.0.50", "ip-address=10.0.0.51,mac-address=fa:16:3e:aa:bb:cc"},
		device:             "dev-9",
		deviceOwner:        "compute:nova",
		host:               "cmp1",
		enablePortSecurity: true,
	}
	flags := changedSet{
		"allowed-address":      true,
		"device":               true,
		"device-owner":         true,
		"host":                 true,
		"enable-port-security": true,
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runPortSet(context.Background(), networkClient(fakeServer), o, "port-1", f, flags, &buf); err != nil {
		t.Fatalf("runPortSet error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
}

func TestRunPortSet_NoAllowedAddressClearsTheList(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports": []}`))
	})
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"port": {"allowed_address_pairs": []}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"port": {"id": "port-1", "network_id": "net-1"}}`))
	})

	f := &portSetFlags{noAllowedAddress: true}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runPortSet(context.Background(), networkClient(fakeServer), o, "port-1", f, changedSet{}, &buf); err != nil {
		t.Fatalf("runPortSet error: %v", err)
	}
}

func TestParseAddressPairs(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    ports.AddressPair
		wantErr string
	}{
		{name: "ip only", spec: "ip-address=10.0.0.5", want: ports.AddressPair{IPAddress: "10.0.0.5"}},
		{
			name: "ip and mac", spec: "ip-address=10.0.0.5,mac-address=fa:16:3e:00:00:01",
			want: ports.AddressPair{IPAddress: "10.0.0.5", MACAddress: "fa:16:3e:00:00:01"},
		},
		{
			name: "underscore keys", spec: "ip_address=10.0.0.5,mac_address=fa:16:3e:00:00:01",
			want: ports.AddressPair{IPAddress: "10.0.0.5", MACAddress: "fa:16:3e:00:00:01"},
		},
		{name: "mac only", spec: "mac-address=fa:16:3e:00:00:01", wantErr: "requires ip-address"},
		{name: "unknown key", spec: "ip-address=10.0.0.5,vlan=7", wantErr: "unknown key"},
		{name: "not key=value", spec: "10.0.0.5", wantErr: "expected key=value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAddressPairs([]string{tc.spec})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseAddressPairs() error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAddressPairs() error = %v", err)
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("parseAddressPairs() = %+v, want [%+v]", got, tc.want)
			}
		})
	}
}

// port unset removes entries from lists neutron can only replace wholesale, so it
// reads the port, filters, and writes back the remainder — guarded by the
// revision number it read.
func TestRunPortUnset_FiltersListsAndPinsRevision(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ports": []}`))
	})
	fakeServer.Mux.HandleFunc("/security-groups", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"security_groups": []}`))
	})
	// buildFixedIPs resolves the subnet reference; an empty match falls back to
	// the literal ID.
	fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subnets": []}`))
	})

	var gotIfMatch string
	fakeServer.Mux.HandleFunc("/ports/port-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"port": {
              "id": "port-1", "name": "p", "network_id": "net-1", "revision_number": 12,
              "fixed_ips": [
                {"subnet_id": "sub-1", "ip_address": "10.0.0.5"},
                {"subnet_id": "sub-2", "ip_address": "10.0.1.5"}
              ],
              "security_groups": ["sg-1", "sg-2"],
              "allowed_address_pairs": [
                {"ip_address": "10.0.0.50", "mac_address": "fa:16:3e:aa:bb:cc"},
                {"ip_address": "10.0.0.51"}
              ]
            }}`))
			return
		}
		gotIfMatch = r.Header.Get("If-Match")
		// Only the entries NOT named for removal survive; the MAC is omitted from
		// the removal spec, so the 10.0.0.50 pair goes regardless of its MAC.
		th.TestJSONRequest(t, r, `{"port": {
          "fixed_ips": [{"subnet_id": "sub-2", "ip_address": "10.0.1.5"}],
          "security_groups": ["sg-2"],
          "allowed_address_pairs": [{"ip_address": "10.0.0.51"}]
        }}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"port": {"id": "port-1", "network_id": "net-1"}}`))
	})

	f := &portUnsetFlags{
		fixedIP:        []string{"subnet=sub-1"},
		securityGroup:  []string{"sg-1"},
		allowedAddress: []string{"ip-address=10.0.0.50"},
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runPortUnset(context.Background(), networkClient(fakeServer), o, "port-1", f, &buf); err != nil {
		t.Fatalf("runPortUnset error: %v", err)
	}
	if gotIfMatch != "revision_number=12" {
		t.Errorf("If-Match = %q, want revision_number=12 so a concurrent change is rejected", gotIfMatch)
	}
}

func TestPortUnset_RequiresAFlag(t *testing.T) {
	cmd := newPortUnsetCommand(nil, &output.Options{Format: output.FormatTable})
	cmd.SetArgs([]string{"port-1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "at least one attribute flag") {
		t.Fatalf("expected a required-flag error, got %v", err)
	}
}

func TestMatchesAnyFixedIP_PartialSpecs(t *testing.T) {
	have := ports.IP{SubnetID: "sub-1", IPAddress: "10.0.0.5"}
	tests := []struct {
		name   string
		remove ports.IP
		want   bool
	}{
		{"subnet only matches", ports.IP{SubnetID: "sub-1"}, true},
		{"address only matches", ports.IP{IPAddress: "10.0.0.5"}, true},
		{"both match", ports.IP{SubnetID: "sub-1", IPAddress: "10.0.0.5"}, true},
		{"wrong subnet", ports.IP{SubnetID: "sub-2"}, false},
		{"wrong address", ports.IP{IPAddress: "10.0.0.6"}, false},
		{"right subnet wrong address", ports.IP{SubnetID: "sub-1", IPAddress: "10.0.0.6"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesAnyFixedIP(have, []ports.IP{tc.remove}); got != tc.want {
				t.Errorf("matchesAnyFixedIP(%+v) = %v, want %v", tc.remove, got, tc.want)
			}
		})
	}
}

// Neutron's rule-type listing takes all_supported / all_rules, but gophercloud's
// ListRuleTypes accepts no options at all, so koc rebuilds the pager with the
// query appended. That the params reach the wire — and that the default request
// still carries none — is the whole contract.
func TestRunQoSRuleTypeList_AllFlagsBecomeQueryParams(t *testing.T) {
	tests := []struct {
		name         string
		allSupported bool
		allRules     bool
		wantQuery    map[string]string
		absent       []string
	}{
		{name: "default", absent: []string{"all_supported", "all_rules"}},
		{name: "--all-supported", allSupported: true,
			wantQuery: map[string]string{"all_supported": "true"}, absent: []string{"all_rules"}},
		{name: "--all-rules", allRules: true,
			wantQuery: map[string]string{"all_rules": "true"}, absent: []string{"all_supported"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			fakeServer.Mux.HandleFunc("/qos/rule-types", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodGet)
				if len(tc.wantQuery) > 0 {
					th.TestFormValues(t, r, tc.wantQuery)
				}
				for _, key := range tc.absent {
					if r.URL.Query().Has(key) {
						t.Errorf("query should not carry %s: %s", key, r.URL.RawQuery)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"rule_types": [{"type": "bandwidth_limit"}]}`))
			})

			o := &output.Options{Format: output.FormatTable}
			var buf bytes.Buffer
			err := runQoSRuleTypeList(context.Background(), networkClient(fakeServer), o,
				tc.allSupported, tc.allRules, &buf)
			if err != nil {
				t.Fatalf("runQoSRuleTypeList error: %v", err)
			}
			if !strings.Contains(buf.String(), "bandwidth_limit") {
				t.Errorf("output missing the rule type\n---\n%s", buf.String())
			}
		})
	}
}

// --target-all-projects is a friendlier spelling of neutron's target_tenant "*";
// the wildcard is what must actually be sent.
func TestRunRBACCreate_TargetAllProjectsSendsWildcard(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/rbac-policies", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"rbac_policy": {
          "action": "access_as_shared", "object_type": "network",
          "object_id": "net-1", "target_tenant": "*"
        }}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"rbac_policy": {"id": "rb-1", "object_type": "network",
          "object_id": "net-1", "action": "access_as_shared", "target_tenant": "*",
          "project_id": "p1"}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runRBACCreate(context.Background(), networkClient(fakeServer), o,
		"net-1", "access_as_shared", "network", rbacAllProjects, &buf)
	if err != nil {
		t.Fatalf("runRBACCreate error: %v", err)
	}
	if !strings.Contains(buf.String(), "rb-1") {
		t.Errorf("output missing the created policy\n---\n%s", buf.String())
	}
}
