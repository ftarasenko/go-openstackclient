package compute_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/keypairs"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/compute"
	"github.com/ftarasenko/go-openstackclient/pkg/openstack/resolve"
)

const (
	flavorID  = "4a3b2c1d-0e9f-4a8b-9c7d-6e5f4a3b2c1d"
	projectID = "6c1f0e2d-3b4a-4c5d-8e9f-0a1b2c3d4e5f"
	userID    = "8d2e1f0a-4b3c-4d5e-9f8a-1b2c3d4e5f6a"
)

// nova is a fake compute endpoint whose client speaks microversion mv, as
// openstack.Session.Compute's does once the caller has pinned it.
func nova(t *testing.T, mv string) (*th.FakeServer, *gophercloud.ServiceClient) {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "compute"
	sc.Microversion = mv
	return &fakeServer, sc
}

func answer(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func flavorJSON(id, name string, public bool) string {
	p := "false"
	if public {
		p = "true"
	}
	return `{"id":"` + id + `","name":"` + name + `","ram":512,"disk":1,"vcpus":1,"swap":0,` +
		`"os-flavor-access:is_public":` + p + `,"OS-FLV-EXT-DATA:ephemeral":0}`
}

// recorder records the requests a fake served, in order.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *recorder) add(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := req.Method + " " + req.URL.Path
	if req.URL.RawQuery != "" {
		line += "?" + req.URL.RawQuery
	}
	r.seen = append(r.seen, line)
}

func (r *recorder) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.seen, ", ")
}

func TestListFlavors_FiltersAndCaps(t *testing.T) {
	fs, sc := nova(t, "2.93")
	fs.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestHeader(t, r, "OpenStack-API-Version", "compute 2.93")
		th.TestFormValues(t, r, map[string]string{"is_public": "None", "minRam": "256", "limit": "2"})
		answer(w, http.StatusOK, `{"flavors":[`+flavorJSON("a", "m1.a", true)+`,`+
			flavorJSON("b", "m1.b", false)+`,`+flavorJSON("c", "m1.c", true)+`]}`)
	})
	all, err := compute.ListFlavors(context.Background(), sc, flavors.ListOpts{
		AccessType: flavors.AllAccess, MinRAM: 256, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "a" || all[1].ID != "b" {
		t.Errorf("Limit must cap the result at 2, got %+v", all)
	}
}

func TestGetFlavor_WrapsWithTheID(t *testing.T) {
	fs, sc := nova(t, "latest")
	fs.Mux.HandleFunc("/flavors/"+flavorID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := compute.GetFlavor(context.Background(), sc, flavorID)
	if err == nil || !strings.HasPrefix(err.Error(), `showing flavor "`+flavorID+`": `) {
		t.Fatalf("err = %v", err)
	}
	if !gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
		t.Error("gophercloud's 404 must stay reachable through the wrap")
	}
}

// flavorViews answers the default listing with public and the is_public=None
// listing with all, recording which views were asked for.
func flavorViews(t *testing.T, public, all []string) (*gophercloud.ServiceClient, *recorder) {
	t.Helper()
	fs, sc := nova(t, "latest")
	rec := &recorder{}
	fs.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		list := public
		if r.URL.Query().Get("is_public") == "None" {
			list = all
		}
		answer(w, http.StatusOK, `{"flavors":[`+strings.Join(list, ",")+`]}`)
	})
	return sc, rec
}

func TestFindFlavor(t *testing.T) {
	pub := flavorJSON(flavorID, "m1.small", true)
	priv := flavorJSON("p1", "gpu.private", false)
	ctx := context.Background()

	t.Run("an ID match wins and needs one listing", func(t *testing.T) {
		sc, rec := flavorViews(t, []string{flavorJSON("x", flavorID, true), pub}, nil)
		fl, err := compute.FindFlavor(ctx, sc, flavorID)
		if err != nil || fl.Name != "m1.small" {
			t.Fatalf("fl = %+v, err = %v", fl, err)
		}
		if rec.all() != "GET /flavors/detail" {
			t.Errorf("requests = %s", rec.all())
		}
	})
	t.Run("a private flavor is found in the second view", func(t *testing.T) {
		sc, rec := flavorViews(t, []string{pub}, []string{pub, priv})
		fl, err := compute.FindFlavor(ctx, sc, "gpu.private")
		if err != nil || fl.ID != "p1" {
			t.Fatalf("fl = %+v, err = %v", fl, err)
		}
		if want := "GET /flavors/detail, GET /flavors/detail?is_public=None"; rec.all() != want {
			t.Errorf("requests = %s, want %s", rec.all(), want)
		}
	})
	t.Run("no match is a NotFoundError", func(t *testing.T) {
		sc, _ := flavorViews(t, []string{pub}, []string{pub})
		_, err := compute.FindFlavor(ctx, sc, "nope")
		var nf *resolve.NotFoundError
		if !errors.As(err, &nf) || nf.Kind != "flavor" || nf.Ref != "nope" || !errors.Is(err, openstack.ErrNotFound) {
			t.Fatalf("err = %#v", err)
		}
	})
	t.Run("several name matches are an AmbiguousError", func(t *testing.T) {
		sc, _ := flavorViews(t, []string{flavorJSON("a", "dup", true), flavorJSON("b", "dup", true)}, nil)
		_, err := compute.FindFlavor(ctx, sc, "dup")
		var amb *resolve.AmbiguousError
		if !errors.As(err, &amb) || amb.Count != 2 {
			t.Fatalf("err = %#v", err)
		}
	})
	t.Run("a failed listing names the reference", func(t *testing.T) {
		fs, sc := nova(t, "latest")
		fs.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		_, err := compute.FindFlavor(ctx, sc, "m1.small")
		if err == nil || !strings.HasPrefix(err.Error(), `resolving flavor "m1.small": `) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCreateAndDeleteFlavor(t *testing.T) {
	fs, sc := nova(t, "2.55")
	fs.Mux.HandleFunc("/flavors", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestHeader(t, r, "OpenStack-API-Version", "compute 2.55")
		th.TestJSONRequest(t, r, `{"flavor":{"name":"m1.tiny","ram":256,"vcpus":1,"disk":0,
			"os-flavor-access:is_public":false,"description":"for tests"}}`)
		answer(w, http.StatusOK, `{"flavor":`+flavorJSON(flavorID, "m1.tiny", false)+`}`)
	})
	fs.Mux.HandleFunc("/flavors/"+flavorID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodDelete)
		w.WriteHeader(http.StatusAccepted)
	})
	disk, public := 0, false
	ctx := context.Background()
	fl, err := compute.CreateFlavor(ctx, sc, flavors.CreateOpts{
		Name: "m1.tiny", RAM: 256, VCPUs: 1, Disk: &disk, IsPublic: &public, Description: "for tests",
	})
	if err != nil || fl.ID != flavorID {
		t.Fatalf("fl = %+v, err = %v", fl, err)
	}
	if err := compute.DeleteFlavor(ctx, sc, flavorID); err != nil {
		t.Fatal(err)
	}
}

func TestCreateFlavor_WrapsWithTheName(t *testing.T) {
	fs, sc := nova(t, "latest")
	fs.Mux.HandleFunc("/flavors", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusConflict, `{"conflictingRequest":{"message":"exists","code":409}}`)
	})
	_, err := compute.CreateFlavor(context.Background(), sc, flavors.CreateOpts{Name: "m1.tiny", RAM: 1, VCPUs: 1})
	if err == nil || !strings.HasPrefix(err.Error(), `creating flavor "m1.tiny": `) {
		t.Fatalf("err = %v", err)
	}
}

func TestFlavorAccess(t *testing.T) {
	fs, sc := nova(t, "latest")
	access := `{"flavor_access":[{"flavor_id":"` + flavorID + `","tenant_id":"` + projectID + `"}]}`
	fs.Mux.HandleFunc("/flavors/"+flavorID+"/os-flavor-access", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		answer(w, http.StatusOK, access)
	})
	var bodies []string
	fs.Mux.HandleFunc("/flavors/"+flavorID+"/action", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		answer(w, http.StatusOK, access)
	})
	ctx := context.Background()
	list, err := compute.ListFlavorAccess(ctx, sc, flavorID)
	if err != nil || len(list) != 1 || list[0].TenantID != projectID {
		t.Fatalf("list = %+v, err = %v", list, err)
	}
	if _, err := compute.AddFlavorAccess(ctx, sc, flavorID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := compute.RemoveFlavorAccess(ctx, sc, flavorID, projectID); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"addTenantAccess":{"tenant":"` + projectID + `"}}`,
		`{"removeTenantAccess":{"tenant":"` + projectID + `"}}`,
	}
	if strings.Join(bodies, "\n") != strings.Join(want, "\n") {
		t.Errorf("bodies = %q, want %q", bodies, want)
	}
}

func TestFlavorAccess_WrapsWithTheIDs(t *testing.T) {
	fs, sc := nova(t, "latest")
	fs.Mux.HandleFunc("/flavors/"+flavorID+"/action", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	_, err := compute.AddFlavorAccess(context.Background(), sc, flavorID, projectID)
	if want := `granting project "` + projectID + `" access to flavor "` + flavorID + `": `; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("err = %v", err)
	}
}

func TestFlavorExtraSpecs(t *testing.T) {
	fs, sc := nova(t, "latest")
	rec := &recorder{}
	fs.Mux.HandleFunc("/flavors/"+flavorID+"/os-extra_specs", func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		switch r.Method {
		case http.MethodGet:
			answer(w, http.StatusOK, `{"extra_specs":{"hw:numa_nodes":"1","aggregate:ssd":"true","hw:cpu_policy":"dedicated"}}`)
		case http.MethodPost:
			th.TestJSONRequest(t, r, `{"extra_specs":{"hw:cpu_policy":"dedicated"}}`)
			answer(w, http.StatusOK, `{"extra_specs":{"hw:cpu_policy":"dedicated"}}`)
		}
	})
	fs.Mux.HandleFunc("/flavors/"+flavorID+"/os-extra_specs/", func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.WriteHeader(http.StatusOK)
	})
	ctx := context.Background()
	set, err := compute.SetFlavorExtraSpecs(ctx, sc, flavorID, map[string]string{"hw:cpu_policy": "dedicated"})
	if err != nil || set["hw:cpu_policy"] != "dedicated" {
		t.Fatalf("set = %v, err = %v", set, err)
	}
	if err := compute.ClearFlavorExtraSpecs(ctx, sc, flavorID); err != nil {
		t.Fatal(err)
	}
	base := "/flavors/" + flavorID + "/os-extra_specs"
	want := "POST " + base + ", GET " + base +
		", DELETE " + base + "/aggregate:ssd, DELETE " + base + "/hw:cpu_policy, DELETE " + base + "/hw:numa_nodes"
	if rec.all() != want {
		t.Errorf("requests =\n%s\nwant (deletes in key order)\n%s", rec.all(), want)
	}
}

func TestDeleteFlavorExtraSpec_WrapsWithKeyAndID(t *testing.T) {
	fs, sc := nova(t, "latest")
	fs.Mux.HandleFunc("/flavors/"+flavorID+"/os-extra_specs/hw:mem", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	err := compute.DeleteFlavorExtraSpec(context.Background(), sc, flavorID, "hw:mem")
	if want := `unsetting property "hw:mem" on flavor "` + flavorID + `": `; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateFlavorDescription_SetsAndClears(t *testing.T) {
	fs, sc := nova(t, "2.55")
	var bodies []string
	fs.Mux.HandleFunc("/flavors/"+flavorID, func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		th.TestHeader(t, r, "OpenStack-API-Version", "compute 2.55")
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		answer(w, http.StatusOK, `{"flavor":`+flavorJSON(flavorID, "m1.tiny", true)+`}`)
	})
	ctx := context.Background()
	if err := compute.UpdateFlavorDescription(ctx, sc, flavorID, "gold"); err != nil {
		t.Fatal(err)
	}
	if err := compute.UpdateFlavorDescription(ctx, sc, flavorID, ""); err != nil {
		t.Fatal(err)
	}
	want := []string{`{"flavor":{"description":"gold"}}`, `{"flavor":{"description":null}}`}
	if strings.Join(bodies, "\n") != strings.Join(want, "\n") {
		t.Errorf("bodies = %q, want %q (an empty description is an explicit null)", bodies, want)
	}
}

func TestUpdateFlavorDescription_WrapsWithTheID(t *testing.T) {
	fs, sc := nova(t, "2.54")
	fs.Mux.HandleFunc("/flavors/"+flavorID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	err := compute.UpdateFlavorDescription(context.Background(), sc, flavorID, "gold")
	if err == nil || !strings.HasPrefix(err.Error(), `setting description on flavor "`+flavorID+`": `) {
		t.Fatalf("err = %v", err)
	}
}

const keypairJSON = `{"name":"ops","fingerprint":"aa:bb","type":"ssh","user_id":"` + userID + `",` +
	`"public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample ops@example.com"}`

func TestKeypairs(t *testing.T) {
	fs, sc := nova(t, "2.10")
	fs.Mux.HandleFunc("/os-keypairs", func(w http.ResponseWriter, r *http.Request) {
		th.TestHeader(t, r, "OpenStack-API-Version", "compute 2.10")
		switch r.Method {
		case http.MethodGet:
			th.TestFormValues(t, r, map[string]string{"user_id": userID})
			answer(w, http.StatusOK, `{"keypairs":[{"keypair":`+keypairJSON+`}]}`)
		case http.MethodPost:
			th.TestJSONRequest(t, r, `{"keypair":{"name":"ops","public_key":"ssh-ed25519 AAAA ops@example.com"}}`)
			answer(w, http.StatusOK, `{"keypair":`+keypairJSON+`}`)
		default:
			t.Errorf("unexpected %s", r.Method)
		}
	})
	fs.Mux.HandleFunc("/os-keypairs/ops", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"user_id": userID})
		switch r.Method {
		case http.MethodGet:
			answer(w, http.StatusOK, `{"keypair":`+keypairJSON+`}`)
		case http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected %s", r.Method)
		}
	})
	ctx := context.Background()
	list, err := compute.ListKeypairs(ctx, sc, keypairs.ListOpts{UserID: userID})
	if err != nil || len(list) != 1 || list[0].Name != "ops" {
		t.Fatalf("list = %+v, err = %v", list, err)
	}
	k, err := compute.GetKeypair(ctx, sc, "ops", keypairs.GetOpts{UserID: userID})
	if err != nil || k.UserID != userID {
		t.Fatalf("k = %+v, err = %v", k, err)
	}
	if _, err := compute.CreateKeypair(ctx, sc, keypairs.CreateOpts{Name: "ops", PublicKey: "ssh-ed25519 AAAA ops@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := compute.DeleteKeypair(ctx, sc, "ops", keypairs.DeleteOpts{UserID: userID}); err != nil {
		t.Fatal(err)
	}
}

func TestKeypairs_WrapWithTheName(t *testing.T) {
	fs, sc := nova(t, "latest")
	fs.Mux.HandleFunc("/os-keypairs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	fs.Mux.HandleFunc("/os-keypairs/ops", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	ctx := context.Background()
	checks := []struct {
		prefix string
		err    error
	}{
		{"listing keypairs: ", func() error { _, err := compute.ListKeypairs(ctx, sc, nil); return err }()},
		{`showing keypair "ops": `, func() error { _, err := compute.GetKeypair(ctx, sc, "ops", nil); return err }()},
		{`creating keypair "ops": `, func() error {
			_, err := compute.CreateKeypair(ctx, sc, keypairs.CreateOpts{Name: "ops"})
			return err
		}()},
		{`deleting keypair "ops": `, compute.DeleteKeypair(ctx, sc, "ops", nil)},
	}
	for _, c := range checks {
		if c.err == nil || !strings.HasPrefix(c.err.Error(), c.prefix) {
			t.Errorf("err = %v, want prefix %q", c.err, c.prefix)
		}
	}
}

// TestErrorsNameTheCall drives every function against a nova that fails
// everything: each error says what it was doing, names the reference it was
// given, and keeps gophercloud's error reachable.
func TestErrorsNameTheCall(t *testing.T) {
	fs, sc := nova(t, "latest")
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusInternalServerError, `{"computeFault":{"code":500,"message":"boom"}}`)
	})
	ctx := context.Background()
	id := `"` + flavorID + `"`
	cases := []struct {
		prefix string
		call   func() error
	}{
		{"listing flavors: ", func() error { _, err := compute.ListFlavors(ctx, sc, flavors.ListOpts{}); return err }},
		{"deleting flavor " + id + ": ", func() error { return compute.DeleteFlavor(ctx, sc, flavorID) }},
		{"listing project access for flavor " + id + ": ", func() error {
			_, err := compute.ListFlavorAccess(ctx, sc, flavorID)
			return err
		}},
		{`revoking project "` + projectID + `" access to flavor ` + id + ": ", func() error {
			_, err := compute.RemoveFlavorAccess(ctx, sc, flavorID, projectID)
			return err
		}},
		{"setting properties on flavor " + id + ": ", func() error {
			_, err := compute.SetFlavorExtraSpecs(ctx, sc, flavorID, map[string]string{"a": "1"})
			return err
		}},
		{"listing properties of flavor " + id + ": ", func() error { return compute.ClearFlavorExtraSpecs(ctx, sc, flavorID) }},
		{"listing keypairs: ", func() error { _, err := compute.ListKeypairs(ctx, sc, nil); return err }},
	}
	for _, c := range cases {
		err := c.call()
		if err == nil || !strings.HasPrefix(err.Error(), c.prefix) {
			t.Errorf("err = %v, want prefix %q", err, c.prefix)
			continue
		}
		if !gophercloud.ResponseCodeIs(err, http.StatusInternalServerError) {
			t.Errorf("%q: gophercloud's 500 must stay reachable", c.prefix)
		}
	}
}

func TestClearFlavorExtraSpecs_StopsAtTheFirstFailedDelete(t *testing.T) {
	fs, sc := nova(t, "latest")
	rec := &recorder{}
	fs.Mux.HandleFunc("/flavors/"+flavorID+"/os-extra_specs", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusOK, `{"extra_specs":{"a":"1","b":"2"}}`)
	})
	fs.Mux.HandleFunc("/flavors/"+flavorID+"/os-extra_specs/", func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.WriteHeader(http.StatusForbidden)
	})
	err := compute.ClearFlavorExtraSpecs(context.Background(), sc, flavorID)
	if want := `unsetting property "a" on flavor "` + flavorID + `": `; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("err = %v", err)
	}
	if rec.all() != "DELETE /flavors/"+flavorID+"/os-extra_specs/a" {
		t.Errorf("requests = %s", rec.all())
	}
}
