package keyvrm_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/pkg/keyvrm"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
)

// List the HA-marked host aggregates of one availability zone, a page at a
// time. The KeyVRM client comes from a Session; here its endpoint is set
// explicitly instead of being looked up in the catalog.
func ExampleListHostAggregates() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Method, r.URL.Path, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"ha-1","availability_zone_name":"az1","host_aggregate_name":"agg1","marker":"HA"}],
			"total":1,"limit":50,"offset":0}`)
	}))
	defer srv.Close()

	s := openstack.FromProvider(&gophercloud.ProviderClient{}, gophercloud.EndpointOpts{},
		openstack.Options{KeyVRMEndpoint: srv.URL})
	sc, err := s.KeyVRM()
	if err != nil {
		fmt.Println(err)
		return
	}
	page, err := keyvrm.ListHostAggregates(context.Background(), sc, keyvrm.ListOpts{
		Limit:   50,
		Filters: map[string]string{"availability_zone_name": "az1", "marker": "HA"},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, ha := range page.Data {
		fmt.Println(ha.ID, ha.HostAggregateName, *ha.Marker)
	}
	fmt.Println("total:", page.Total)
	// Output:
	// GET /v1/host_aggregates availability_zone_name=az1&limit=50&marker=HA
	// ha-1 agg1 HA
	// total: 1
}
