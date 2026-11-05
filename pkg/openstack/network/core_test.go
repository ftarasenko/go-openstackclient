package network_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/floatingips"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/security/rules"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/network"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

const (
	netID = "6f1c4b2a-0d3e-4f5a-8b9c-1d2e3f4a5b6c"
	fipID = "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d"
	sgID  = "1b2c3d4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e"
)

type req struct{ Method, Path, Query, IfMatch, Body string }

type reply struct {
	Status int
	Body   string
}

// fakeNeutron answers "METHOD /path?query" or "METHOD /path" from routes, a
// NeutronError 404 otherwise, and records every request.
func fakeNeutron(t *testing.T, routes map[string]reply) (*[]req, *gophercloud.ServiceClient) {
	t.Helper()
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	var seen []req
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		var v any
		if json.Unmarshal(raw, &v) == nil {
			b, _ := json.Marshal(v)
			body = string(b)
		}
		seen = append(seen, req{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("If-Match"), body})
		w.Header().Set("Content-Type", "application/json")
		key := r.Method + " " + r.URL.Path
		out, ok := routes[key+"?"+r.URL.RawQuery]
		if !ok {
			out, ok = routes[key]
		}
		if !ok {
			out = reply{http.StatusNotFound, `{"NeutronError":{"type":"NotFound","message":"not found"}}`}
		}
		if out.Status == 0 {
			out.Status = http.StatusOK
		}
		w.WriteHeader(out.Status)
		_, _ = io.WriteString(w, out.Body)
	})
	sc := fakeclient.ServiceClient(fs)
	sc.Type = "network"
	return &seen, sc
}

func is404(t *testing.T, err error, prefix string) {
	t.Helper()
	if !gophercloud.ResponseCodeIs(err, http.StatusNotFound) || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %v; want the 404 behind %q", err, prefix)
	}
}

func TestFindIDs(t *testing.T) {
	finds := []struct {
		kind, path, filter string
		find               func(context.Context, *gophercloud.ServiceClient, string) (string, error)
	}{
		{"network", "/networks", "name", network.FindNetworkID},
		{"subnet", "/subnets", "name", network.FindSubnetID},
		{"router", "/routers", "name", network.FindRouterID},
		{"port", "/ports", "name", network.FindPortID},
		{"security group", "/security-groups", "name", network.FindSecurityGroupID},
		{"address scope", "/address-scopes", "name", network.FindAddressScopeID},
		{"address group", "/address-groups", "name", network.FindAddressGroupID},
		{"floating IP", "/floatingips", "floating_ip_address", network.FindFloatingIPID},
		{"QoS policy", "/qos/policies", "name", network.FindQoSPolicyID},
		{"subnet pool", "/subnetpools", "name", network.FindSubnetPoolID},
		{"network trunk", "/trunks", "name", network.FindTrunkID},
		{"network segment", "/segments", "name", network.FindSegmentID},
	}
	for _, f := range finds {
		t.Run(f.kind, func(t *testing.T) {
			// gophercloud's collection keys: floatingips, security_groups, policies, …
			key := strings.ReplaceAll(f.path[strings.LastIndex(f.path, "/")+1:], "-", "_")
			// subnetpools.SubnetPool's UnmarshalJSON needs the prefix lengths.
			prefixes := `,"default_prefixlen":8,"min_prefixlen":8,"max_prefixlen":32`
			item := func(id string) string { return `{"id":"` + id + `"` + prefixes + `}` }
			seen, sc := fakeNeutron(t, map[string]reply{
				"GET " + f.path + "?" + f.filter + "=one":  {Body: `{"` + key + `":[` + item("id-1") + `]}`},
				"GET " + f.path + "?" + f.filter + "=none": {Body: `{"` + key + `":[]}`},
				"GET " + f.path + "?" + f.filter + "=two":  {Body: `{"` + key + `":[` + item("a") + `,` + item("b") + `]}`},
			})
			ctx := context.Background()
			if id, err := f.find(ctx, sc, netID); id != netID || err != nil || len(*seen) != 0 {
				t.Errorf("UUID = %q, %v after %d requests", id, err, len(*seen))
			}
			if id, err := f.find(ctx, sc, "one"); id != "id-1" || err != nil {
				t.Errorf("one = %q, %v", id, err)
			}
			_, err := f.find(ctx, sc, "none")
			var nf *resolve.NotFoundError
			if !errors.As(err, &nf) || !errors.Is(err, openstack.ErrNotFound) || err.Error() != `no `+f.kind+` found for "none"` {
				t.Errorf("none = %v", err)
			}
			_, err = f.find(ctx, sc, "two")
			var amb *resolve.AmbiguousError
			if !errors.As(err, &amb) || amb.Count != 2 || amb.Kind != f.kind || amb.Ref != "two" {
				t.Errorf("two = %v", err)
			}
			_, err = f.find(ctx, sc, "down")
			is404(t, err, `looking up `+f.kind+` "down": `)
		})
	}
}

func TestFloatingIPs(t *testing.T) {
	fip := `{"id":"` + fipID + `","floating_ip_address":"203.0.113.5","floating_network_id":"` + netID +
		`","qos_policy_id":"qos-1","dns_name":"web","dns_domain":"example.com."}`
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /floatingips":             {Body: `{"floatingips":[` + fip + `]}`},
		"GET /floatingips/" + fipID:    {Body: `{"floatingip":` + fip + `}`},
		"POST /floatingips":            {Status: http.StatusCreated, Body: `{"floatingip":` + fip + `}`},
		"PUT /floatingips/" + fipID:    {Body: `{"floatingip":` + fip + `}`},
		"DELETE /floatingips/" + fipID: {Status: http.StatusNoContent},
	})
	ctx := context.Background()
	all, err := network.ListFloatingIPs(ctx, sc, network.FloatingIPListOpts{
		ListOpts:  floatingips.ListOpts{Status: "ACTIVE"},
		RouterIDs: []string{"r1", "r2"},
	})
	if err != nil || len(all) != 1 || all[0].FloatingIP.FloatingIP != "203.0.113.5" || all[0].QoSPolicyID != "qos-1" || all[0].DNSDomain != "example.com." {
		t.Fatalf("ListFloatingIPs = %+v, %v", all, err)
	}
	if f, err := network.GetFloatingIP(ctx, sc, fipID); err != nil || f.DNSName != "web" {
		t.Fatalf("GetFloatingIP = %+v, %v", f, err)
	}
	created, err := network.CreateFloatingIP(ctx, sc, network.WithFloatingIPCreateAttrs(
		floatingips.CreateOpts{FloatingNetworkID: netID}, map[string]any{"qos_policy_id": "qos-1"}))
	if err != nil || created.ID != fipID {
		t.Fatalf("CreateFloatingIP = %+v, %v", created, err)
	}
	rev, desc := 7, "typed"
	if _, err := network.UpdateFloatingIP(ctx, sc, fipID, network.WithFloatingIPUpdateAttrs(
		floatingips.UpdateOpts{Description: &desc, RevisionNumber: &rev},
		map[string]any{"description": "extra", "qos_policy_id": nil})); err != nil {
		t.Fatalf("UpdateFloatingIP: %v", err)
	}
	if err := network.DeleteFloatingIP(ctx, sc, fipID); err != nil {
		t.Fatalf("DeleteFloatingIP: %v", err)
	}
	want := []req{
		{"GET", "/floatingips", "router_id=r1&router_id=r2&status=ACTIVE", "", ""},
		{"GET", "/floatingips/" + fipID, "", "", ""},
		{"POST", "/floatingips", "", "", `{"floatingip":{"floating_network_id":"` + netID + `","qos_policy_id":"qos-1"}}`},
		// The extra attributes win over the typed ones, and the revision guard survives the wrapper.
		{"PUT", "/floatingips/" + fipID, "", "revision_number=7", `{"floatingip":{"description":"extra","qos_policy_id":null}}`},
		{"DELETE", "/floatingips/" + fipID, "", "", ""},
	}
	assertRequests(t, *seen, want)

	_, bad := fakeNeutron(t, map[string]reply{"GET /floatingips": {Body: `{"floatingips":[{"id":"f","qos_policy_id":1}]}`}})
	if _, err := network.ListFloatingIPs(ctx, bad, floatingips.ListOpts{}); err == nil || !strings.HasPrefix(err.Error(), "parsing floating IP list: ") {
		t.Errorf("malformed list: %v", err)
	}
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListFloatingIPs(ctx, gone, floatingips.ListOpts{})), "listing floating IPs: ")
	is404(t, second(network.GetFloatingIP(ctx, gone, "f")), "getting floating IP f: ")
	is404(t, second(network.CreateFloatingIP(ctx, gone, floatingips.CreateOpts{FloatingNetworkID: "n"})), "creating floating IP: ")
	is404(t, second(network.UpdateFloatingIP(ctx, gone, "f", floatingips.UpdateOpts{})), "updating floating IP f: ")
	is404(t, network.DeleteFloatingIP(ctx, gone, "f"), "deleting floating IP f: ")
}

func TestSecurityGroups(t *testing.T) {
	group := `{"id":"` + sgID + `","name":"web","shared":true,"security_group_rules":[{"id":"r1","direction":"ingress","normalized_cidr":"0.0.0.0/0"}]}`
	rule := `{"id":"r1","direction":"ingress","ethertype":"IPv4","security_group_id":"` + sgID + `"}`
	seen, sc := fakeNeutron(t, map[string]reply{
		"GET /security-groups":                 {Body: `{"security_groups":[` + group + `]}`},
		"GET /security-groups/" + sgID:         {Body: `{"security_group":` + group + `}`},
		"POST /security-groups":                {Status: http.StatusCreated, Body: `{"security_group":` + group + `}`},
		"PUT /security-groups/" + sgID:         {Body: `{"security_group":` + group + `}`},
		"DELETE /security-groups/" + sgID:      {Status: http.StatusNoContent},
		"GET /security-group-rules":            {Body: `{"security_group_rules":[` + rule + `]}`},
		"GET /security-group-rules/r1":         {Body: `{"security_group_rule":` + rule + `}`},
		"POST /security-group-rules":           {Status: http.StatusCreated, Body: `{"security_group_rule":` + rule + `}`},
		"DELETE /security-group-rules/r1":      {Status: http.StatusNoContent},
		"PUT /networks/" + netID + "/tags":     {Body: `{"tags":["a","b"]}`},
		"GET /security-groups?name=bad-format": {Body: `{"security_groups":[{"id":"g","shared":"yes"}]}`},
	})
	ctx := context.Background()
	shared := true
	all, err := network.ListSecurityGroups(ctx, sc, network.SecurityGroupListOpts{ListOpts: groups.ListOpts{Name: "web"}, Shared: &shared})
	if err != nil || len(all) != 1 || all[0].Shared == nil || !*all[0].Shared || all[0].RawRules != nil {
		t.Fatalf("ListSecurityGroups = %+v, %v", all, err)
	}
	g, err := network.GetSecurityGroup(ctx, sc, sgID)
	if err != nil || g.Name != "web" || len(g.Rules) != 1 || g.RawRules[0]["normalized_cidr"] != "0.0.0.0/0" {
		t.Fatalf("GetSecurityGroup = %+v, %v", g, err)
	}
	if _, err := network.CreateSecurityGroup(ctx, sc, network.WithSecurityGroupCreateAttrs(groups.CreateOpts{Name: "web"}, map[string]any{"x": 1})); err != nil {
		t.Fatalf("CreateSecurityGroup: %v", err)
	}
	if _, err := network.UpdateSecurityGroup(ctx, sc, sgID, groups.UpdateOpts{Name: "web2"}); err != nil {
		t.Fatalf("UpdateSecurityGroup: %v", err)
	}
	if err := network.DeleteSecurityGroup(ctx, sc, sgID); err != nil {
		t.Fatalf("DeleteSecurityGroup: %v", err)
	}
	if rs, err := network.ListSecurityGroupRules(ctx, sc, rules.ListOpts{SecGroupID: sgID}); err != nil || len(rs) != 1 {
		t.Fatalf("ListSecurityGroupRules = %+v, %v", rs, err)
	}
	if r, err := network.GetSecurityGroupRule(ctx, sc, "r1"); err != nil || r.SecGroupID != sgID {
		t.Fatalf("GetSecurityGroupRule = %+v, %v", r, err)
	}
	if _, err := network.CreateSecurityGroupRule(ctx, sc, network.WithSecurityGroupRuleCreateAttrs(
		rules.CreateOpts{Direction: rules.DirIngress, EtherType: rules.EtherType4, SecGroupID: sgID, Protocol: rules.ProtocolICMP},
		map[string]any{"port_range_min": 0})); err != nil {
		t.Fatalf("CreateSecurityGroupRule: %v", err)
	}
	if err := network.DeleteSecurityGroupRule(ctx, sc, "r1"); err != nil {
		t.Fatalf("DeleteSecurityGroupRule: %v", err)
	}
	if tags, err := network.ReplaceTags(ctx, sc, "networks", netID, []string{"a", "b"}); err != nil || len(tags) != 2 {
		t.Fatalf("ReplaceTags = %v, %v", tags, err)
	}
	want := []req{
		{"GET", "/security-groups", "name=web&shared=true", "", ""},
		{"GET", "/security-groups/" + sgID, "", "", ""},
		{"POST", "/security-groups", "", "", `{"security_group":{"name":"web","x":1}}`},
		{"PUT", "/security-groups/" + sgID, "", "", `{"security_group":{"name":"web2"}}`},
		{"DELETE", "/security-groups/" + sgID, "", "", ""},
		{"GET", "/security-group-rules", "security_group_id=" + sgID, "", ""},
		{"GET", "/security-group-rules/r1", "", "", ""},
		{"POST", "/security-group-rules", "", "", `{"security_group_rule":{"direction":"ingress","ethertype":"IPv4","port_range_min":0,"protocol":"icmp","security_group_id":"` + sgID + `"}}`},
		{"DELETE", "/security-group-rules/r1", "", "", ""},
		{"PUT", "/networks/" + netID + "/tags", "", "", `{"tags":["a","b"]}`},
	}
	assertRequests(t, *seen, want)

	if _, err := network.ListSecurityGroups(ctx, sc, network.SecurityGroupListOpts{ListOpts: groups.ListOpts{Name: "bad-format"}}); err == nil ||
		!strings.HasPrefix(err.Error(), "parsing security group list: ") {
		t.Errorf("malformed list: %v", err)
	}
	_, gone := fakeNeutron(t, nil)
	is404(t, second(network.ListSecurityGroups(ctx, gone, network.SecurityGroupListOpts{})), "listing security groups: ")
	is404(t, second(network.GetSecurityGroup(ctx, gone, "g")), "getting security group g: ")
	is404(t, second(network.CreateSecurityGroup(ctx, gone, groups.CreateOpts{Name: "g"})), "creating security group: ")
	is404(t, second(network.UpdateSecurityGroup(ctx, gone, "g", groups.UpdateOpts{Name: "g"})), "updating security group g: ")
	is404(t, network.DeleteSecurityGroup(ctx, gone, "g"), "deleting security group g: ")
	is404(t, second(network.ListSecurityGroupRules(ctx, gone, rules.ListOpts{})), "listing security group rules: ")
	is404(t, second(network.GetSecurityGroupRule(ctx, gone, "r")), "getting security group rule r: ")
	is404(t, second(network.CreateSecurityGroupRule(ctx, gone, rules.CreateOpts{Direction: rules.DirIngress, EtherType: rules.EtherType4, SecGroupID: "g"})), "creating security group rule: ")
	is404(t, network.DeleteSecurityGroupRule(ctx, gone, "r"), "deleting security group rule r: ")
	is404(t, second(network.ReplaceTags(ctx, gone, "ports", "p", []string{"x"})), "setting tags on ports p: ")
}

func assertRequests(t *testing.T, seen, want []req) {
	t.Helper()
	if len(seen) != len(want) {
		t.Fatalf("requests = %+v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("request %d = %+v\n want %+v", i, seen[i], want[i])
		}
	}
}
