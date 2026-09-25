package network

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Upstream's list carries four ip_availability_details columns, empty where the
// network-ip-availability-details extension is absent.
func TestRunIPAvailabilityList_RendersTheDetailColumns(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/network-ip-availabilities", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"network_ip_availabilities":[
		  {"network_id":"n1","network_name":"with","total_ips":256,"used_ips":3,
		   "ip_availability_details":{"total_ips_in_subnet":256,"total_ips_in_allocation_pool":253,
		     "used_ips_in_subnet":3,"used_ips_in_allocation_pool":1}},
		  {"network_id":"n2","network_name":"without","total_ips":16,"used_ips":2}]}`)
	})
	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runIPAvailabilityList(context.Background(), networkClient(fakeServer), o, 0, "", &buf); err != nil {
		t.Fatalf("runIPAvailabilityList: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{
		"Network ID,Network Name,Total IPs,Used IPs,Total IPs in Subnet,Total IPs in Allocation Pool," +
			"Used IPs in Subnet,Used IPs in Allocation Pool",
		"n1,with,256,3,256,253,3,1",
		"n2,without,16,2,,,,",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("output =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
