package crossservice

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// The tests in this file execute commands through cobra — Execute() → RunE →
// NewSession → runXxx — rather than calling a runXxx seam directly, the same way
// internal/cli/network/rune_exec_test.go does. Here it buys more than glue
// coverage: these nouns merge two or three services, and only a run that serves
// all of them at once shows whether each client was derived and each endpoint
// reached. A seam test with one fake client cannot.
//
// Every service is served by one mock, told apart by a per-service path prefix
// the endpoint locator hands out, so a request landing on the wrong service is
// visible as a wrong path rather than as an accidental pass.
func execCrossservice(t *testing.T, fakeServer th.FakeServer, argv ...string) (string, error) {
	t.Helper()

	a := &auth.Options{}
	provider := &gophercloud.ProviderClient{
		TokenID: "fake-token",
		// Identity is reached through IdentityBase rather than the locator
		// (gophercloud derives it as IdentityBase + "v3/"), so both are set.
		IdentityBase:     fakeServer.Server.URL + "/identity/",
		IdentityEndpoint: fakeServer.Server.URL + "/identity/",
		EndpointLocator: func(eo gophercloud.EndpointOpts) (string, error) {
			return fakeServer.Server.URL + "/" + eo.Type + "/", nil
		},
	}
	a.SetAuthenticatorForTest(func(context.Context) (*auth.Client, error) {
		return a.NewClientForTest(provider, gophercloud.EndpointOpts{}), nil
	})

	root := &cobra.Command{Use: "koc"}
	root.AddCommand(NewCommands(a, &output.Options{Format: "table"})...)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(argv)
	root.SilenceUsage = true
	root.SilenceErrors = true
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

// serve registers a handler that answers one path with one body and records that
// it was asked, plus the query it was asked with.
func serve(t *testing.T, fakeServer th.FakeServer, path, body string) (hits *int, query *string) {
	t.Helper()
	var n int
	var q string
	fakeServer.Mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		n++
		q = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	return &n, &q
}

func TestExec_LimitsShowAbsolute_MergesComputeAndVolume(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	computeHits, _ := serve(t, fakeServer, "/compute/limits",
		`{"limits":{"absolute":{"maxTotalInstances":10,"totalCoresUsed":3}}}`)
	volumeHits, _ := serve(t, fakeServer, "/block-storage/limits",
		`{"limits":{"absolute":{"maxTotalVolumes":7,"totalGigabytesUsed":42}}}`)

	out, err := execCrossservice(t, fakeServer, "limits", "show", "--absolute")
	if err != nil {
		t.Fatal(err)
	}
	if *computeHits != 1 || *volumeHits != 1 {
		t.Errorf("compute hit %d times, volume %d; want 1 each", *computeHits, *volumeHits)
	}
	// One table, both services' names in it — the merge upstream performs.
	for _, want := range []string{"maxTotalInstances", "10", "maxTotalVolumes", "7", "totalGigabytesUsed", "42"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// A 2xx whose body carries no "limits" object makes gophercloud's Extract
// return (nil, nil). Passing that on used to segfault; it has to be an error
// naming the service instead.
func TestExec_LimitsShowAbsolute_EmptyBodyIsAnErrorNotAPanic(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serve(t, fakeServer, "/compute/limits", `{}`)
	serve(t, fakeServer, "/block-storage/limits", `{"limits":{"absolute":{}}}`)

	_, err := execCrossservice(t, fakeServer, "limits", "show", "--absolute")
	if err == nil || !strings.Contains(err.Error(), "getting compute limits") {
		t.Fatalf("err = %v, want one naming the compute limits fetch", err)
	}
}

// --project is admin-only and has to reach nova as tenant_id, which means
// resolving the name through keystone first. A wiring slip here silently
// reports the caller's own limits instead of the project's.
func TestExec_LimitsShow_ProjectRefIsResolvedAndForwarded(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	_, projectQuery := serve(t, fakeServer, "/identity/v3/projects",
		`{"projects":[{"id":"proj-99","name":"demo"}]}`)
	_, computeQuery := serve(t, fakeServer, "/compute/limits", `{"limits":{"absolute":{}}}`)
	serve(t, fakeServer, "/block-storage/limits", `{"limits":{"absolute":{}}}`)

	if _, err := execCrossservice(t, fakeServer, "limits", "show", "--absolute", "--project", "demo"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*projectQuery, "name=demo") {
		t.Errorf("keystone was queried with %q, want a name=demo filter", *projectQuery)
	}
	if !strings.Contains(*computeQuery, "tenant_id=proj-99") {
		t.Errorf("nova was queried with %q, want the resolved tenant_id", *computeQuery)
	}
}

// Nova hardcodes an empty rate array and cinder's is config-driven, so the
// normal answer is an empty table — which must still be a table, not an error.
func TestExec_LimitsShowRate_ReadsBothServices(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	computeHits, _ := serve(t, fakeServer, "/compute/limits", `{"limits":{"rate":[]}}`)
	volumeHits, _ := serve(t, fakeServer, "/block-storage/limits", `{"limits":{"rate":[
	  {"uri":"*","regex":".*","limit":[
	    {"verb":"POST","value":10,"remaining":9,"unit":"MINUTE","next-available":"2026-09-22T00:00:00Z"}]}]}}`)

	out, err := execCrossservice(t, fakeServer, "limits", "show", "--rate")
	if err != nil {
		t.Fatal(err)
	}
	if *computeHits != 1 || *volumeHits != 1 {
		t.Errorf("compute hit %d times, volume %d; want 1 each", *computeHits, *volumeHits)
	}
	for _, want := range []string{"volume", "POST", "MINUTE"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestExec_AvailabilityZoneList_MergesThreeServicesAndFiltersToOne(t *testing.T) {
	const (
		computeBody = `{"availabilityZoneInfo":[{"zoneName":"nova","zoneState":{"available":true}}]}`
		volumeBody  = `{"availabilityZoneInfo":[{"zoneName":"cinder-az","zoneState":{"available":false}}]}`
		networkBody = `{"availability_zones":[{"name":"neutron-az","resource":"network","state":"available"}]}`
	)

	tests := []struct {
		name       string
		argv       []string
		wantHits   [3]int // compute, volume, network
		wantInOut  []string
		wantNotOut []string
	}{
		{
			name:      "no flag lists every service",
			argv:      []string{"availability", "zone", "list"},
			wantHits:  [3]int{1, 1, 1},
			wantInOut: []string{"nova", "compute", "cinder-az", "volume", "neutron-az", "network"},
		},
		{
			name:       "--compute asks nova only",
			argv:       []string{"availability", "zone", "list", "--compute"},
			wantHits:   [3]int{1, 0, 0},
			wantInOut:  []string{"nova"},
			wantNotOut: []string{"cinder-az", "neutron-az"},
		},
		{
			name:       "--volume asks cinder only",
			argv:       []string{"availability", "zone", "list", "--volume"},
			wantHits:   [3]int{0, 1, 0},
			wantInOut:  []string{"cinder-az", "not available"},
			wantNotOut: []string{"nova", "neutron-az"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			c, _ := serve(t, fakeServer, "/compute/os-availability-zone", computeBody)
			v, _ := serve(t, fakeServer, "/block-storage/os-availability-zone", volumeBody)
			n, _ := serve(t, fakeServer, "/network/v2.0/availability_zones", networkBody)

			out, err := execCrossservice(t, fakeServer, tt.argv...)
			if err != nil {
				t.Fatal(err)
			}
			if got := [3]int{*c, *v, *n}; got != tt.wantHits {
				t.Errorf("endpoint hits = %v, want %v", got, tt.wantHits)
			}
			for _, want := range tt.wantInOut {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
			for _, unwanted := range tt.wantNotOut {
				if strings.Contains(out, unwanted) {
					t.Errorf("output should not mention %q:\n%s", unwanted, out)
				}
			}
		})
	}
}

// --long swaps nova's plain listing for the admin detail endpoint; it must not
// change which services are asked.
func TestExec_AvailabilityZoneList_LongUsesTheDetailEndpoint(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	plain, _ := serve(t, fakeServer, "/compute/os-availability-zone", `{"availabilityZoneInfo":[]}`)
	detail, _ := serve(t, fakeServer, "/compute/os-availability-zone/detail",
		`{"availabilityZoneInfo":[{"zoneName":"nova","zoneState":{"available":true}}]}`)

	if _, err := execCrossservice(t, fakeServer, "availability", "zone", "list", "--compute", "--long"); err != nil {
		t.Fatal(err)
	}
	if *detail != 1 || *plain != 0 {
		t.Errorf("detail hit %d times, plain %d; want 1 and 0", *detail, *plain)
	}
}
