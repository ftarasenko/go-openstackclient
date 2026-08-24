package network

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/layer3/routers"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// The three --key=value spec parsers each split into a pure per-spec parse and a
// caller that resolves names. These tests drive the pure halves directly, which
// is where the key tables and their aliases live.

func TestParseSubportSpec(t *testing.T) {
	tests := []struct {
		name     string
		spec     string
		wantPort string
		wantType string
		wantID   int
		wantErr  string
	}{
		{name: "hyphenated keys", spec: "port=p1,segmentation-type=vlan,segmentation-id=7",
			wantPort: "p1", wantType: "vlan", wantID: 7},
		{name: "underscore keys", spec: "port=p1,segmentation_type=vlan,segmentation_id=7",
			wantPort: "p1", wantType: "vlan", wantID: 7},
		{name: "surrounding whitespace is trimmed", spec: " port=p1 , segmentation-type=vlan , segmentation-id= 7 ",
			wantPort: "p1", wantType: "vlan", wantID: 7},
		{name: "empty parts are skipped", spec: "port=p1,,segmentation-type=vlan,segmentation-id=7,",
			wantPort: "p1", wantType: "vlan", wantID: 7},
		{name: "the last value for a key wins", spec: "port=p1,port=p2,segmentation-type=vlan,segmentation-id=7",
			wantPort: "p2", wantType: "vlan", wantID: 7},
		{name: "a part without = is rejected", spec: "port=p1,vlan", wantErr: "expected key=value"},
		{name: "unknown key", spec: "port=p1,vlan=7", wantErr: `unknown key "vlan"`},
		{name: "non-numeric segmentation id", spec: "port=p1,segmentation-type=vlan,segmentation-id=x",
			wantErr: "is not a number"},
		{name: "missing port", spec: "segmentation-type=vlan,segmentation-id=7",
			wantErr: "requires port, segmentation-type and a non-zero segmentation-id"},
		{name: "missing type", spec: "port=p1,segmentation-id=7",
			wantErr: "requires port, segmentation-type and a non-zero segmentation-id"},
		// Neutron has no VLAN 0, so a zero ID is the "not given" signal rather
		// than a value; it must fail validation like an absent key.
		{name: "zero segmentation id", spec: "port=p1,segmentation-type=vlan,segmentation-id=0",
			wantErr: "requires port, segmentation-type and a non-zero segmentation-id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sp, portRef, err := parseSubportSpec(tc.spec)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseSubportSpec() error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSubportSpec() error = %v", err)
			}
			if portRef != tc.wantPort {
				t.Errorf("port ref = %q, want %q", portRef, tc.wantPort)
			}
			if sp.SegmentationType != tc.wantType || sp.SegmentationID != tc.wantID {
				t.Errorf("subport = %+v, want type %q id %d", sp, tc.wantType, tc.wantID)
			}
			// PortID is the caller's job; the spec parse must leave it empty.
			if sp.PortID != "" {
				t.Errorf("PortID = %q, want it left for the resolver", sp.PortID)
			}
		})
	}
}

func TestParseRouteSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    routers.Route
		wantErr string
	}{
		{name: "destination and gateway", spec: "destination=10.0.0.0/8,gateway=192.0.2.1",
			want: routers.Route{DestinationCIDR: "10.0.0.0/8", NextHop: "192.0.2.1"}},
		// nexthop is the API's own spelling of the key OSC calls gateway.
		{name: "nexthop alias", spec: "destination=10.0.0.0/8,nexthop=192.0.2.1",
			want: routers.Route{DestinationCIDR: "10.0.0.0/8", NextHop: "192.0.2.1"}},
		{name: "order does not matter", spec: "gateway=192.0.2.1,destination=10.0.0.0/8",
			want: routers.Route{DestinationCIDR: "10.0.0.0/8", NextHop: "192.0.2.1"}},
		{name: "whitespace is trimmed", spec: " destination=10.0.0.0/8 , gateway=192.0.2.1 ",
			want: routers.Route{DestinationCIDR: "10.0.0.0/8", NextHop: "192.0.2.1"}},
		{name: "missing gateway", spec: "destination=10.0.0.0/8", wantErr: "requires both"},
		{name: "missing destination", spec: "gateway=192.0.2.1", wantErr: "requires both"},
		{name: "a part without = is rejected", spec: "destination=10.0.0.0/8,gateway", wantErr: "expected key=value"},
		{name: "unknown key", spec: "destination=10.0.0.0/8,gateway=192.0.2.1,metric=5", wantErr: `unknown key "metric"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRouteSpec(tc.spec)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseRouteSpec() error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRouteSpec() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("parseRouteSpec() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseFixedIPSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    fixedIPSpec
		wantErr string
	}{
		{name: "subnet only", spec: "subnet=public-sub",
			want: fixedIPSpec{subnetRef: "public-sub", subnetSet: true}},
		{name: "subnet and ip", spec: "subnet=public-sub,ip-address=192.0.2.10",
			want: fixedIPSpec{subnetRef: "public-sub", subnetSet: true, ipAddress: "192.0.2.10"}},
		{name: "subnet-id alias", spec: "subnet-id=s1", want: fixedIPSpec{subnetRef: "s1", subnetSet: true}},
		{name: "subnet_id alias", spec: "subnet_id=s1", want: fixedIPSpec{subnetRef: "s1", subnetSet: true}},
		{name: "ip_address alias", spec: "subnet=s1,ip_address=192.0.2.10",
			want: fixedIPSpec{subnetRef: "s1", subnetSet: true, ipAddress: "192.0.2.10"}},
		// An explicit but empty subnet= is recorded as present, so the caller
		// still runs it through resolution rather than short-circuiting.
		{name: "empty subnet value is still set", spec: "subnet=", want: fixedIPSpec{subnetSet: true}},
		{name: "no subnet key at all", spec: "ip-address=192.0.2.10", want: fixedIPSpec{ipAddress: "192.0.2.10"}},
		{name: "a part without = is rejected", spec: "subnet=s1,192.0.2.10", wantErr: "expected key=value"},
		{name: "unknown key", spec: "subnet=s1,gateway=192.0.2.1", wantErr: `unknown key "gateway"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFixedIPSpec(tc.spec)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseFixedIPSpec() error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFixedIPSpec() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("parseFixedIPSpec() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// parseExternalFixedIPs is the half that resolves subnet names against neutron.
func TestParseExternalFixedIPs_ResolvesSubnetNames(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotNames []string
	fakeServer.Mux.HandleFunc("/subnets", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		name := r.URL.Query().Get("name")
		gotNames = append(gotNames, name)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		body := `{"subnets": []}`
		if name == "public-sub" {
			body = `{"subnets": [{"id": "55555555-5555-5555-5555-555555555555", "name": "public-sub"}]}`
		}
		_, _ = w.Write([]byte(body))
	})

	got, err := parseExternalFixedIPs(context.Background(), networkClient(fakeServer), []string{
		"subnet=public-sub,ip-address=192.0.2.10",
		// A UUID has no matching name, so the zero-match fallback keeps it as-is.
		"subnet=66666666-6666-6666-6666-666666666666",
	})
	if err != nil {
		t.Fatalf("parseExternalFixedIPs() error = %v", err)
	}
	want := []routers.ExternalFixedIP{
		{SubnetID: "55555555-5555-5555-5555-555555555555", IPAddress: "192.0.2.10"},
		{SubnetID: "66666666-6666-6666-6666-666666666666"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseExternalFixedIPs() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("fixed ip %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(gotNames) != 2 || gotNames[0] != "public-sub" {
		t.Errorf("subnet lookups = %v, want one per spec starting with public-sub", gotNames)
	}
}

// An empty spec list must stay nil so external_gateway_info omits the key.
func TestParseExternalFixedIPs_EmptyIsNil(t *testing.T) {
	got, err := parseExternalFixedIPs(context.Background(), nil, nil)
	if err != nil || got != nil {
		t.Fatalf("parseExternalFixedIPs(nil) = %v, %v; want nil, nil", got, err)
	}
}

// A spec with no subnet= at all is rejected before any neutron call.
func TestParseExternalFixedIPs_RequiresSubnet(t *testing.T) {
	_, err := parseExternalFixedIPs(context.Background(), nil, []string{"ip-address=192.0.2.10"})
	if err == nil || !strings.Contains(err.Error(), `--fixed-ip "ip-address=192.0.2.10" requires subnet=`) {
		t.Fatalf("parseExternalFixedIPs() error = %v, want the requires-subnet message", err)
	}
}
