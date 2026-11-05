package dns_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/dns"
)

// An admin lists another project's recordsets: the scope rides on the
// client, so every call made with it carries the sudo header.
func ExampleWithScope() {
	designateAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Method, r.URL.Path, "as", r.Header.Get("X-Auth-Sudo-Project-ID"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v2/zones" {
			_, _ = io.WriteString(w, `{"zones":[{"id":"zone-1","name":"example.com."}],"links":{}}`)
			return
		}
		_, _ = io.WriteString(w, `{"recordsets":[{"id":"rs-1","name":"www.example.com.","type":"A",`+
			`"records":["192.0.2.10"],"ttl":null}],"links":{}}`)
	}))
	defer designateAPI.Close()

	pc := &gophercloud.ProviderClient{
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) { return designateAPI.URL + "/", nil },
	}
	sess := openstack.FromProvider(pc, gophercloud.EndpointOpts{Region: "RegionOne"}, openstack.Options{})
	dc, err := sess.DNS()
	if err != nil {
		fmt.Println(err)
		return
	}
	dc = dns.WithScope(dc, dns.Scope{SudoProjectID: "project-b"})

	ctx := context.Background()
	zoneID, err := dns.FindZoneID(ctx, dc, "example.com")
	if err != nil {
		fmt.Println(err)
		return
	}
	all, err := dns.ListRecordSets(ctx, dc, zoneID, nil, 0)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, rs := range all {
		fmt.Println(rs.Name, rs.Type, rs.Records, "inherits the zone TTL:", rs.NullableTTL == nil)
	}
	// Output:
	// GET /v2/zones as project-b
	// GET /v2/zones/zone-1/recordsets as project-b
	// www.example.com. A [192.0.2.10] inherits the zone TTL: true
}
