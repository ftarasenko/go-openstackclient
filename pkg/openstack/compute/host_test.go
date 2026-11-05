package compute_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/aggregates"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/services"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/compute"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

const aggregatesJSON = `{"aggregates":[
	{"id":1,"name":"ssd","availability_zone":"az1","hosts":["cmp-01","cmp-02"],"metadata":{}},
	{"id":2,"name":"gpu","availability_zone":"az1","hosts":["cmp-02"],"metadata":{}},
	{"id":3,"name":"dup","hosts":[],"metadata":{}},
	{"id":4,"name":"dup","hosts":[],"metadata":{}}]}`

const aggregateJSON = `{"aggregate":{"id":1,"name":"ssd","availability_zone":"az1","hosts":["cmp-01"],"metadata":{"k":"v"}}}`

func TestFindAggregateID(t *testing.T) {
	fs, sc := nova(t, "latest")
	rec := &recorder{}
	fs.Mux.HandleFunc("/os-aggregates", func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		answer(w, http.StatusOK, aggregatesJSON)
	})
	ctx := context.Background()
	if id, err := compute.FindAggregateID(ctx, sc, "7"); err != nil || id != 7 || rec.all() != "" {
		t.Errorf("a numeric ref is the ID without a request: id = %d, err = %v, requests = %s", id, err, rec.all())
	}
	if id, err := compute.FindAggregateID(ctx, sc, "gpu"); err != nil || id != 2 {
		t.Errorf("gpu: id = %d, err = %v", id, err)
	}
	_, err := compute.FindAggregateID(ctx, sc, "nope")
	var nf *resolve.NotFoundError
	if !errors.As(err, &nf) || nf.Kind != "aggregate" {
		t.Errorf("nope: err = %#v", err)
	}
	_, err = compute.FindAggregateID(ctx, sc, "dup")
	var amb *resolve.AmbiguousError
	if !errors.As(err, &amb) || amb.Count != 2 {
		t.Errorf("dup: err = %#v", err)
	}
}

func TestHostAggregates(t *testing.T) {
	fs, sc := nova(t, "latest")
	fs.Mux.HandleFunc("/os-aggregates", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusOK, aggregatesJSON)
	})
	got, err := compute.HostAggregates(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got["cmp-02"], ",") != "ssd,gpu" || strings.Join(got["cmp-01"], ",") != "ssd" || len(got) != 2 {
		t.Errorf("got %v", got)
	}
}

func TestAggregateWrites(t *testing.T) {
	fs, sc := nova(t, "2.93")
	var seen []string
	record := func(r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+string(b))
	}
	fs.Mux.HandleFunc("/os-aggregates", func(w http.ResponseWriter, r *http.Request) {
		th.TestHeader(t, r, "OpenStack-API-Version", "compute 2.93")
		record(r)
		answer(w, http.StatusOK, aggregateJSON)
	})
	fs.Mux.HandleFunc("/os-aggregates/1", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		answer(w, http.StatusOK, aggregateJSON)
	})
	fs.Mux.HandleFunc("/os-aggregates/1/action", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		answer(w, http.StatusOK, aggregateJSON)
	})
	ctx := context.Background()
	steps := []func() error{
		func() error {
			_, err := compute.CreateAggregate(ctx, sc, aggregates.CreateOpts{Name: "ssd", AvailabilityZone: "az1"})
			return err
		},
		func() error { _, err := compute.GetAggregate(ctx, sc, 1); return err },
		func() error {
			_, err := compute.UpdateAggregate(ctx, sc, 1, aggregates.UpdateOpts{Name: "fast"})
			return err
		},
		func() error {
			_, err := compute.SetAggregateMetadata(ctx, sc, 1, map[string]any{"k": "v", "gone": nil})
			return err
		},
		func() error { _, err := compute.AddAggregateHost(ctx, sc, 1, "cmp-01"); return err },
		func() error { _, err := compute.RemoveAggregateHost(ctx, sc, 1, "cmp-01"); return err },
		func() error { return compute.DeleteAggregate(ctx, sc, 1) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	want := []string{
		`POST /os-aggregates {"aggregate":{"availability_zone":"az1","name":"ssd"}}`,
		`GET /os-aggregates/1 `,
		`PUT /os-aggregates/1 {"aggregate":{"name":"fast"}}`,
		`POST /os-aggregates/1/action {"set_metadata":{"metadata":{"gone":null,"k":"v"}}}`,
		`POST /os-aggregates/1/action {"add_host":{"host":"cmp-01"}}`,
		`POST /os-aggregates/1/action {"remove_host":{"host":"cmp-01"}}`,
		`DELETE /os-aggregates/1 `,
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}

func TestAggregateErrorsNameTheID(t *testing.T) {
	fs, sc := nova(t, "latest")
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusConflict, `{"conflictingRequest":{"code":409,"message":"no"}}`)
	})
	ctx := context.Background()
	cases := []struct {
		prefix string
		call   func() error
	}{
		{"listing aggregates: ", func() error { _, err := compute.ListAggregates(ctx, sc); return err }},
		{"listing aggregates: ", func() error { _, err := compute.HostAggregates(ctx, sc); return err }},
		{"listing aggregates: ", func() error { _, err := compute.FindAggregateID(ctx, sc, "ssd"); return err }},
		{"showing aggregate 1: ", func() error { _, err := compute.GetAggregate(ctx, sc, 1); return err }},
		{`creating aggregate "ssd": `, func() error {
			_, err := compute.CreateAggregate(ctx, sc, aggregates.CreateOpts{Name: "ssd"})
			return err
		}},
		{"updating aggregate 1: ", func() error {
			_, err := compute.UpdateAggregate(ctx, sc, 1, aggregates.UpdateOpts{Name: "x"})
			return err
		}},
		{"setting properties on aggregate 1: ", func() error {
			_, err := compute.SetAggregateMetadata(ctx, sc, 1, map[string]any{"k": "v"})
			return err
		}},
		{`adding host "h" to aggregate 1: `, func() error { _, err := compute.AddAggregateHost(ctx, sc, 1, "h"); return err }},
		{`removing host "h" from aggregate 1: `, func() error { _, err := compute.RemoveAggregateHost(ctx, sc, 1, "h"); return err }},
		{"deleting aggregate 1: ", func() error { return compute.DeleteAggregate(ctx, sc, 1) }},
	}
	for _, c := range cases {
		if err := c.call(); err == nil || !strings.HasPrefix(err.Error(), c.prefix) || !gophercloud.ResponseCodeIs(err, http.StatusConflict) {
			t.Errorf("err = %v, want prefix %q around the 409", err, c.prefix)
		}
	}
}

// hypervisorsAt answers GET /os-hypervisors/detail like nova does at 2.1 (no
// microversion header) and at 2.53+ (UUIDs, no usage fields), recording the
// microversion header of every listing.
func hypervisorsAt(t *testing.T, mv string, legacy, current string) (*gophercloud.ServiceClient, *[]string) {
	t.Helper()
	fs, sc := nova(t, mv)
	var versions []string
	fs.Mux.HandleFunc("/os-hypervisors/detail", func(w http.ResponseWriter, r *http.Request) {
		v := r.Header.Get("OpenStack-API-Version")
		versions = append(versions, v)
		if v == "" {
			answer(w, http.StatusOK, legacy)
			return
		}
		answer(w, http.StatusOK, current)
	})
	return sc, &versions
}

const hvUUID = "5b6a7c8d-9e0f-4a1b-8c2d-3e4f5a6b7c8d"

const hvLegacy = `{"hypervisors":[
	{"id":1,"hypervisor_version":1,"hypervisor_hostname":"cmp-01","vcpus":64,"vcpus_used":10,"service":{"id":11,"host":"cmp-01"}},
	{"id":2,"hypervisor_version":1,"hypervisor_hostname":"cmp-02","vcpus":64,"vcpus_used":20,"service":{"id":12,"host":"cmp-02"}},
	{"id":3,"hypervisor_version":1,"hypervisor_hostname":"twin","service":{"id":13,"host":"a"}},
	{"id":4,"hypervisor_version":1,"hypervisor_hostname":"twin","service":{"id":14,"host":"b"}}]}`

const hvCurrent = `{"hypervisors":[
	{"id":"` + hvUUID + `","hypervisor_version":1,"hypervisor_hostname":"cmp-02","service":{"id":"svc-uuid-2","host":"cmp-02"}}]}`

func TestListHypervisorsWithUsage_ReadsAt21(t *testing.T) {
	sc, versions := hypervisorsAt(t, "latest", hvLegacy, hvCurrent)
	all, err := compute.ListHypervisorsWithUsage(context.Background(), sc)
	if err != nil || len(all) != 4 || all[0].VCPUsUsed != 10 {
		t.Fatalf("all = %+v, err = %v", all, err)
	}
	if strings.Join(*versions, ",") != "" || sc.Microversion != "latest" {
		t.Errorf("listing must go out without a microversion and leave the client's alone: %q, %q", *versions, sc.Microversion)
	}
}

func TestFindHypervisor(t *testing.T) {
	ctx := context.Background()
	t.Run("by hostname, at 2.1", func(t *testing.T) {
		sc, versions := hypervisorsAt(t, "latest", hvLegacy, hvCurrent)
		h, err := compute.FindHypervisor(ctx, sc, "cmp-01")
		if err != nil || h.ID != "1" || h.VCPUs != 64 || len(*versions) != 1 {
			t.Fatalf("h = %+v, err = %v, listings = %q", h, err, *versions)
		}
	})
	t.Run("a UUID from the current listing maps back to the 2.1 record", func(t *testing.T) {
		sc, versions := hypervisorsAt(t, "2.93", hvLegacy, hvCurrent)
		h, err := compute.FindHypervisor(ctx, sc, hvUUID)
		if err != nil || h.ID != "2" {
			t.Fatalf("h = %+v, err = %v", h, err)
		}
		if strings.Join(*versions, ",") != ",compute 2.93" {
			t.Errorf("listings = %q", *versions)
		}
	})
	t.Run("without a microversion there is no second listing", func(t *testing.T) {
		sc, versions := hypervisorsAt(t, "", hvLegacy, hvCurrent)
		_, err := compute.FindHypervisor(ctx, sc, hvUUID)
		var nf *resolve.NotFoundError
		if !errors.As(err, &nf) || nf.Kind != "hypervisor" || len(*versions) != 1 {
			t.Fatalf("err = %#v, listings = %q", err, *versions)
		}
	})
	t.Run("a shared hostname is ambiguous", func(t *testing.T) {
		sc, _ := hypervisorsAt(t, "latest", hvLegacy, hvCurrent)
		_, err := compute.FindHypervisor(ctx, sc, "twin")
		var amb *resolve.AmbiguousError
		if !errors.As(err, &amb) || amb.Count != 2 {
			t.Fatalf("err = %#v", err)
		}
	})
	t.Run("a failed listing", func(t *testing.T) {
		fs, sc := nova(t, "latest")
		fs.Mux.HandleFunc("/os-hypervisors/detail", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		if _, err := compute.FindHypervisor(ctx, sc, "cmp-01"); err == nil || !strings.HasPrefix(err.Error(), "listing hypervisors: ") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a failed second listing", func(t *testing.T) {
		fs, sc := nova(t, "2.93")
		fs.Mux.HandleFunc("/os-hypervisors/detail", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("OpenStack-API-Version") != "" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			answer(w, http.StatusOK, hvLegacy)
		})
		if _, err := compute.FindHypervisor(ctx, sc, hvUUID); err == nil || !strings.HasPrefix(err.Error(), "listing hypervisors: ") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestHypervisorIDs(t *testing.T) {
	ctx := context.Background()
	sc, _ := hypervisorsAt(t, "2.93", hvLegacy, hvCurrent)
	legacy, err := compute.ListHypervisorsWithUsage(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	if id, svc, ok := compute.HypervisorIDs(ctx, sc, legacy[1]); !ok || id != hvUUID || svc != "svc-uuid-2" {
		t.Errorf("cmp-02 = %q, %q, %v", id, svc, ok)
	}
	if id, svc, ok := compute.HypervisorIDs(ctx, sc, legacy[0]); ok || id != "1" || svc != "11" {
		t.Errorf("cmp-01 has no current record: %q, %q, %v", id, svc, ok)
	}
	sc.Microversion = ""
	if id, _, ok := compute.HypervisorIDs(ctx, sc, legacy[1]); ok || id != "2" {
		t.Errorf("without a microversion the 2.1 IDs stand: %q, %v", id, ok)
	}
}

func TestPlacementUsage(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	pc := fakeclient.ServiceClient(fakeServer)
	pc.Type = "placement"
	pc.Microversion = "1.39"
	fakeServer.Mux.HandleFunc("/resource_providers", func(w http.ResponseWriter, r *http.Request) {
		th.TestHeader(t, r, "OpenStack-API-Version", "placement 1.39")
		answer(w, http.StatusOK, `{"resource_providers":[
			{"uuid":"rp-1","name":"cmp-01"},{"uuid":"rp-2","name":"cmp-02"},{"uuid":"rp-3","name":"cmp-03"}]}`)
	})
	fakeServer.Mux.HandleFunc("/resource_providers/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/resource_providers/"), "/")
		switch {
		case parts[0] == "rp-3":
			w.WriteHeader(http.StatusInternalServerError)
		case parts[1] == "inventories":
			answer(w, http.StatusOK, `{"resource_provider_generation":1,"inventories":{"VCPU":{"total":64,"allocation_ratio":4.0}}}`)
		default:
			answer(w, http.StatusOK, `{"resource_provider_generation":1,"usages":{"VCPU":12}}`)
		}
	})
	got, err := compute.PlacementUsage(context.Background(), pc, []string{"cmp-01", "cmp-02", "cmp-03", "cmp-unknown"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "cmp-01,cmp-02" {
		t.Fatalf("hosts = %v; a failed read and an unknown host are left out", names)
	}
	if u := got["cmp-01"]; u.UUID != "rp-1" || u.Inventories["VCPU"].Total != 64 || u.Usages["VCPU"] != 12 {
		t.Errorf("cmp-01 = %+v", u)
	}
}

func TestPlacementUsage_FailedListing(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/resource_providers", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := compute.PlacementUsage(context.Background(), fakeclient.ServiceClient(fakeServer), []string{"cmp-01"})
	if err == nil || !strings.HasPrefix(err.Error(), "listing resource providers: ") {
		t.Fatalf("err = %v", err)
	}
}

const servicesJSON = `{"services":[
	{"id":"s1","binary":"nova-compute","host":"cmp-01","zone":"nova","status":"enabled","state":"up",
	 "admin_state":"MaintenanceMode","error_details":"fan"},
	{"id":"s2","binary":"nova-compute","host":"cmp-02","zone":"nova","status":"disabled","state":"down"}]}`

func TestListServices_AlignsTheExtension(t *testing.T) {
	fs, sc := nova(t, "2.93")
	fs.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"binary": "nova-compute"})
		answer(w, http.StatusOK, servicesJSON)
	})
	all, ext, err := compute.ListServices(context.Background(), sc, services.ListOpts{Binary: "nova-compute"})
	if err != nil || len(all) != 2 || len(ext) != 2 {
		t.Fatalf("all = %+v, ext = %+v, err = %v", all, ext, err)
	}
	if ext[0].AdminState != "MaintenanceMode" || ext[0].ErrorDetails != "fan" || ext[1] != (compute.ServiceAdminState{}) {
		t.Errorf("ext = %+v", ext)
	}
}

func TestFindServiceID(t *testing.T) {
	fs, sc := nova(t, "2.93")
	fs.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"host": r.URL.Query().Get("host"), "binary": "nova-compute"})
		answer(w, http.StatusOK, servicesJSON)
	})
	ctx := context.Background()
	if id, err := compute.FindServiceID(ctx, sc, "cmp-02", "nova-compute"); err != nil || id != "s2" {
		t.Errorf("id = %q, err = %v", id, err)
	}
	_, err := compute.FindServiceID(ctx, sc, "cmp-09", "nova-compute")
	var nf *resolve.NotFoundError
	if !errors.As(err, &nf) || nf.Ref != "cmp-09/nova-compute" {
		t.Errorf("err = %#v", err)
	}
}

func TestServiceUpdateOpts(t *testing.T) {
	up, down := false, true
	cases := []struct {
		opts compute.ServiceUpdateOpts
		want string
	}{
		{compute.ServiceUpdateOpts{ForcedDown: &up}, `{"forced_down":false}`},
		{compute.ServiceUpdateOpts{Status: services.ServiceDisabled, DisabledReason: "maintenance", ForcedDown: &down},
			`{"disabled_reason":"maintenance","forced_down":true,"status":"disabled"}`},
		{compute.ServiceUpdateOpts{AdminState: "Error", ErrorDetails: "psu"}, `{"admin_state":"Error","error_details":"psu"}`},
		{compute.ServiceUpdateOpts{}, `{}`},
	}
	for _, c := range cases {
		fs, sc := nova(t, "2.93")
		fs.Mux.HandleFunc("/os-services/s1", func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodPut)
			th.TestHeader(t, r, "OpenStack-API-Version", "compute 2.93")
			th.TestJSONRequest(t, r, c.want)
			answer(w, http.StatusOK, `{"service":{"id":"s1","binary":"nova-compute","host":"cmp-01"}}`)
		})
		if _, err := compute.UpdateService(context.Background(), sc, "s1", c.opts); err != nil {
			t.Errorf("%+v: %v", c.opts, err)
		}
	}
}

func TestServiceErrorsNameTheCall(t *testing.T) {
	fs, sc := nova(t, "latest")
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	ctx := context.Background()
	cases := []struct {
		prefix string
		call   func() error
	}{
		{"listing compute services: ", func() error { _, _, err := compute.ListServices(ctx, sc, nil); return err }},
		{"resolving compute service cmp-01/nova-compute: ", func() error {
			_, err := compute.FindServiceID(ctx, sc, "cmp-01", "nova-compute")
			return err
		}},
		{"updating compute service s1: ", func() error {
			_, err := compute.UpdateService(ctx, sc, "s1", compute.ServiceUpdateOpts{Status: services.ServiceEnabled})
			return err
		}},
		{"deleting compute service s1: ", func() error { return compute.DeleteService(ctx, sc, "s1") }},
	}
	for _, c := range cases {
		if err := c.call(); err == nil || !strings.HasPrefix(err.Error(), c.prefix) || !gophercloud.ResponseCodeIs(err, http.StatusForbidden) {
			t.Errorf("err = %v, want prefix %q around the 403", err, c.prefix)
		}
	}
}
