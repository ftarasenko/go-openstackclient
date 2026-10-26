package openstack_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
)

// sessionAt is a Session whose every catalog lookup answers url, with the
// looked-up options recorded in *seen.
func sessionAt(url string, seen *gophercloud.EndpointOpts, opts openstack.Options) *openstack.Session {
	pc := &gophercloud.ProviderClient{
		TokenID: fakeclient.TokenID,
		EndpointLocator: func(eo gophercloud.EndpointOpts) (string, error) {
			if seen != nil {
				*seen = eo
			}
			return url, nil
		},
	}
	return openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, opts)
}

func TestServiceConstructors_TypeAndMicroversion(t *testing.T) {
	s := sessionAt("https://cloud.example.com/", nil, openstack.Options{})
	for _, tc := range []struct {
		name   string
		build  func() (*gophercloud.ServiceClient, error)
		typ    string
		microv string
	}{
		{"Baremetal", s.Baremetal, "baremetal", "latest"},
		{"Introspection", s.Introspection, "baremetal-introspection", ""},
		{"Compute", s.Compute, "compute", "latest"},
		{"Identity", s.Identity, "identity", ""},
		{"Volume", s.Volume, "block-storage", "latest"},
		{"DNS", s.DNS, "dns", ""},
		{"Image", s.Image, "image", ""},
		{"Network", s.Network, "network", ""},
		{"Placement", s.Placement, "placement", "latest"},
		{"LoadBalancer", s.LoadBalancer, "load-balancer", ""},
		{"KeyVRM", s.KeyVRM, "keyvrm", ""},
		{"KeyManager", s.KeyManager, "key-manager", "1.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := tc.build()
			if err != nil {
				t.Fatal(err)
			}
			if sc.Type != tc.typ {
				t.Errorf("Type = %q, want %q", sc.Type, tc.typ)
			}
			if sc.Microversion != tc.microv {
				t.Errorf("Microversion = %q, want %q", sc.Microversion, tc.microv)
			}
		})
	}
}

func TestServiceConstructors_PinnedMicroversions(t *testing.T) {
	s := sessionAt("https://cloud.example.com/", nil, openstack.Options{Microversions: openstack.Microversions{
		Compute: "2.79", Volume: "3.70", Baremetal: "1.80", Placement: "1.39",
	}})
	for _, tc := range []struct {
		build func() (*gophercloud.ServiceClient, error)
		want  string
	}{
		{s.Compute, "2.79"}, {s.Volume, "3.70"}, {s.Baremetal, "1.80"}, {s.Placement, "1.39"},
	} {
		sc, err := tc.build()
		if err != nil {
			t.Fatal(err)
		}
		if sc.Microversion != tc.want {
			t.Errorf("%s: Microversion = %q, want %q", sc.Type, sc.Microversion, tc.want)
		}
	}
}

func TestMicroversionReachesTheWire(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	var got string
	fakeServer.Mux.HandleFunc("/servers", func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("OpenStack-API-Version")
	})
	s := sessionAt(fakeServer.Endpoint(), nil, openstack.Options{Microversions: openstack.Microversions{Compute: "2.93"}})
	sc, err := s.Compute()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := sc.Get(context.Background(), sc.ServiceURL("servers"), nil, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if got != "compute 2.93" {
		t.Errorf("OpenStack-API-Version = %q, want %q", got, "compute 2.93")
	}
}

func TestForRegion_SharesTheTokenAndSwapsTheRegion(t *testing.T) {
	var seen gophercloud.EndpointOpts
	s := sessionAt("https://cloud.example.com/", &seen, openstack.Options{})
	other := s.ForRegion("RegionTwo")

	if other.Provider() != s.Provider() {
		t.Error("ForRegion must share the ProviderClient")
	}
	if s.Region() != "RegionOne" || other.Region() != "RegionTwo" {
		t.Errorf("regions = %q, %q; ForRegion must not change its receiver", s.Region(), other.Region())
	}
	if _, err := other.Network(); err != nil {
		t.Fatal(err)
	}
	if seen.Region != "RegionTwo" || seen.Type != "network" {
		t.Errorf("catalog lookup = %+v, want network in RegionTwo", seen)
	}
}

func TestServiceError(t *testing.T) {
	pc := &gophercloud.ProviderClient{EndpointLocator: func(eo gophercloud.EndpointOpts) (string, error) {
		return "", fmt.Errorf("no %s endpoint in region %s", eo.Type, eo.Region)
	}}
	s := openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionTwo"}, openstack.Options{})
	_, err := s.Image()
	var se *openstack.ServiceError
	if !errors.As(err, &se) || se.Service != "image" {
		t.Fatalf("err = %v (%T), want a *ServiceError for image", err, err)
	}
	if got, want := err.Error(), "creating image client: no image endpoint in region RegionTwo"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestKeyVRM_EndpointOverrideSkipsTheCatalog(t *testing.T) {
	pc := &gophercloud.ProviderClient{EndpointLocator: func(gophercloud.EndpointOpts) (string, error) {
		return "", errors.New("the catalog must not be consulted")
	}}
	s := openstack.FromProvider(pc, gophercloud.EndpointOpts{}, openstack.Options{KeyVRMEndpoint: "https://keyvrm.example.com:9000"})
	sc, err := s.KeyVRM()
	if err != nil {
		t.Fatal(err)
	}
	if sc.Endpoint != "https://keyvrm.example.com:9000/" || sc.ResourceBase != "https://keyvrm.example.com:9000/v1/" {
		t.Errorf("Endpoint = %q, ResourceBase = %q", sc.Endpoint, sc.ResourceBase)
	}
}

func TestNew_AuthOptions(t *testing.T) {
	fc := newFakeCloud(t)
	s, err := openstack.New(context.Background(), openstack.Config{
		AuthOptions: &gophercloud.AuthOptions{
			IdentityEndpoint:            fc.URL + "/v3/",
			ApplicationCredentialID:     "app-cred-id",
			ApplicationCredentialSecret: "app-cred-secret",
		},
		Region: "RegionTwo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.ScopedProjectID() != fakeProjectID {
		t.Errorf("ScopedProjectID = %q, want %q", s.ScopedProjectID(), fakeProjectID)
	}
	sc, err := s.Image()
	if err != nil {
		t.Fatal(err)
	}
	if want := fc.URL + "/r2/image/v2/"; sc.ResourceBaseURL() != want {
		t.Errorf("image endpoint = %q, want %q", sc.ResourceBaseURL(), want)
	}
	if sc.TokenID != fakeToken {
		t.Errorf("token = %q, want the one Keystone issued", sc.TokenID)
	}
}

func TestNew_NamedCloud(t *testing.T) {
	fc := newFakeCloud(t)
	cloudsYAML := fmt.Sprintf(`clouds:
  example:
    auth:
      auth_url: %s/v3/
      username: alice
      password: secret
      project_name: demo
      user_domain_name: Default
      project_domain_name: Default
    region_name: RegionOne
`, fc.URL)
	s, err := openstack.New(context.Background(), openstack.Config{Cloud: "example", CloudsYAML: []byte(cloudsYAML)})
	if err != nil {
		t.Fatal(err)
	}
	if s.Region() != "RegionOne" {
		t.Errorf("Region = %q, want the cloud entry's", s.Region())
	}
	if !strings.Contains(fmt.Sprint(fc.tokenReq), "alice") {
		t.Errorf("token request %v does not carry the cloud's user", fc.tokenReq)
	}

	// Region overrides the entry's region_name.
	s, err = openstack.New(context.Background(), openstack.Config{Cloud: "example", CloudsYAML: []byte(cloudsYAML), Region: "RegionTwo"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Region() != "RegionTwo" {
		t.Errorf("Region = %q, want RegionTwo", s.Region())
	}
}

func TestNew_RejectsAmbiguousConfig(t *testing.T) {
	ao := &gophercloud.AuthOptions{IdentityEndpoint: "https://keystone.example.com/v3/"}
	for name, cfg := range map[string]openstack.Config{
		"nothing":            {},
		"both sources":       {AuthOptions: ao, Cloud: "example"},
		"cloud without yaml": {Cloud: "example"},
		"bad interface":      {AuthOptions: ao, Interface: "privateURL"},
		"tls and client":     {AuthOptions: ao, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}, HTTPClient: &http.Client{}},
	} {
		if _, err := openstack.New(context.Background(), cfg); err == nil {
			t.Errorf("%s: New accepted %+v", name, cfg)
		}
	}
}

func TestScopedProjectID_WithoutKeystone(t *testing.T) {
	if got := openstack.FromProvider(&gophercloud.ProviderClient{}, gophercloud.EndpointOpts{}, openstack.Options{}).ScopedProjectID(); got != "" {
		t.Errorf("ScopedProjectID = %q, want empty for a provider with no auth result", got)
	}
	var s *openstack.Session
	if got := s.ScopedProjectID(); got != "" {
		t.Errorf("nil Session: ScopedProjectID = %q", got)
	}
}
