package quota

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"
	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// quotaSession builds a session whose three service factories point at the given
// mocks. Nova and cinder BOTH serve quotas at /os-quota-sets/<project>, so a
// multi-service test must give them separate servers — sharing one would let the
// compute handler answer the volume request and silently pass. Pass a single
// server when only one service is exercised.
func quotaSession(compute, volume, network th.FakeServer) *session {
	computeClient := fakeclient.ServiceClient(compute)
	computeClient.Type = "compute"
	computeClient.Microversion = "latest"
	volumeClient := fakeclient.ServiceClient(volume)
	volumeClient.Type = "volume"
	volumeClient.Microversion = "latest"
	networkClient := fakeclient.ServiceClient(network)
	networkClient.Type = "network"

	return &session{
		compute: func() (*gophercloud.ServiceClient, error) { return computeClient, nil },
		volume:  func() (*gophercloud.ServiceClient, error) { return volumeClient, nil },
		network: func() (*gophercloud.ServiceClient, error) { return networkClient, nil },
	}
}

// oneServerSession is quotaSession for a test that only exercises one service;
// the other two factories point at the same mock, which registers nothing for
// them so an unexpected call 404s.
func oneServerSession(fakeServer th.FakeServer) *session {
	return quotaSession(fakeServer, fakeServer, fakeServer)
}

const (
	computeQuotaBody = `{"quota_set": {
      "id": "p1", "cores": 64, "instances": 20, "ram": 131072,
      "key_pairs": 100, "metadata_items": 128,
      "server_groups": 10, "server_group_members": 10
    }}`
	volumeQuotaBody = `{"quota_set": {
      "id": "p1", "volumes": 50, "snapshots": 30, "gigabytes": 4000,
      "per_volume_gigabytes": 500, "backups": 10, "backup_gigabytes": 2000, "groups": 5
    }}`
	networkQuotaBody = `{"quota": {
      "network": 25, "subnet": 30, "subnetpool": 5, "port": 500, "router": 15,
      "floatingip": 60, "security_group": 20, "security_group_rule": 200,
      "rbac_policy": 10, "trunk": 40
    }}`
)

func TestRunQuotaShow_MergesAllThreeServices(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	volumeServer := th.SetupHTTP()
	defer volumeServer.Teardown()
	networkServer := th.SetupHTTP()
	defer networkServer.Teardown()

	var hits []string
	fakeServer.Mux.HandleFunc("/os-quota-sets/p1", func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "compute")
		th.TestMethod(t, r, http.MethodGet)
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(computeQuotaBody))
	})
	volumeServer.Mux.HandleFunc("/os-quota-sets/p1", func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "volume")
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(volumeQuotaBody))
	})
	networkServer.Mux.HandleFunc("/quotas/p1", func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "network")
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(networkQuotaBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	sel := serviceSelection{}.resolved()
	s := quotaSession(fakeServer, volumeServer, networkServer)
	if err := runQuotaShow(context.Background(), s, o, "p1", false, sel, &buf); err != nil {
		t.Fatalf("runQuotaShow error: %v", err)
	}
	if len(hits) != 3 {
		t.Errorf("expected all three services queried, got %v", hits)
	}

	out := buf.String()
	for _, want := range []string{
		// compute
		"cores", "64", "instances", "20", "ram", "131072",
		// volume
		"gigabytes", "4000", "volumes", "50", "snapshots", "30",
		// network
		"ports", "500", "routers", "15", "floatingips", "60", "trunks", "40",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("merged quota output missing %q\n---\n%s", want, out)
		}
	}
}

// Selecting one service must not touch the other two catalogs.
func TestRunQuotaShow_ServiceSelectionSkipsOthers(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/quotas/p1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(networkQuotaBody))
	})
	// Compute/volume endpoints are deliberately unregistered: touching them 404s.

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	sel := serviceSelection{network: true}.resolved()
	err := runQuotaShow(context.Background(), oneServerSession(fakeServer), o, "p1", false, sel, &buf)
	if err != nil {
		t.Fatalf("runQuotaShow --network error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ports") {
		t.Errorf("--network output missing network quotas\n---\n%s", out)
	}
	if strings.Contains(out, "cores") {
		t.Errorf("--network output must not carry compute quotas\n---\n%s", out)
	}
}

// The compute defaults endpoint has no typed gophercloud call, so it is fetched
// raw; the URL is the thing worth pinning down.
func TestRunQuotaShow_DefaultUsesDefaultsEndpoints(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/os-quota-sets/p1/defaults", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(computeQuotaBody))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	sel := serviceSelection{compute: true}.resolved()
	err := runQuotaShow(context.Background(), oneServerSession(fakeServer), o, "p1", true, sel, &buf)
	if err != nil {
		t.Fatalf("runQuotaShow --default error: %v", err)
	}
	if gotPath != "/os-quota-sets/p1/defaults" {
		t.Errorf("path = %q, want /os-quota-sets/p1/defaults", gotPath)
	}
}

// setFlagSet builds the quota set flag surface and parses args against it, the
// same way the cobra command does.
func setFlagSet(t *testing.T, args []string) (*quotaSetFlags, *pflag.FlagSet) {
	t.Helper()
	f := &quotaSetFlags{}
	fl := pflag.NewFlagSet("quota set", pflag.ContinueOnError)
	for _, group := range [][]quotaFlag{f.computeFlags(), f.volumeFlags(), f.networkFlags()} {
		for _, qf := range group {
			fl.IntVar(qf.dest, qf.name, 0, "")
		}
	}
	fl.BoolVar(&f.force, "force", false, "")
	if err := fl.Parse(args); err != nil {
		t.Fatalf("parsing %v: %v", args, err)
	}
	return f, fl
}

// A quota nobody named must never appear in the request body — otherwise every
// "quota set --cores 64" would silently reset instances, ram and the rest to 0.
func TestRunQuotaSet_OnlySendsGivenQuotas(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/os-quota-sets/p1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"quota_set": {"cores": 64, "ram": 131072}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(computeQuotaBody))
	})

	f, fl := setFlagSet(t, []string{"--cores=64", "--ram=131072"})
	given := f.givenBy(fl)
	if !given.compute || given.volume || given.network {
		t.Fatalf("givenBy() = %+v, want compute only", given)
	}

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runQuotaSet(context.Background(), oneServerSession(fakeServer), o, "p1", f, fl, given, &buf)
	if err != nil {
		t.Fatalf("runQuotaSet error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if !strings.Contains(buf.String(), "cores") {
		t.Errorf("output missing the updated compute quotas\n---\n%s", buf.String())
	}
}

// Zero is a meaningful quota (deny everything), so an explicit --instances 0 must
// be sent rather than dropped as an unset value.
func TestRunQuotaSet_ExplicitZeroIsSent(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-quota-sets/p1", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{"quota_set": {"instances": 0}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(computeQuotaBody))
	})

	f, fl := setFlagSet(t, []string{"--instances=0"})
	given := f.givenBy(fl)
	if !given.compute {
		t.Fatalf("givenBy() = %+v, want compute selected for an explicit zero", given)
	}

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runQuotaSet(context.Background(), oneServerSession(fakeServer), o, "p1", f, fl, given, &buf); err != nil {
		t.Fatalf("runQuotaSet error: %v", err)
	}
}

// One command line spanning services must fan out to each API with only that
// service's keys.
func TestRunQuotaSet_FansOutAcrossServices(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	volumeServer := th.SetupHTTP()
	defer volumeServer.Teardown()
	networkServer := th.SetupHTTP()
	defer networkServer.Teardown()

	var computeCalled, volumeCalled, networkCalled bool
	// Each service gets only its own keys: nothing volume- or network-shaped may
	// reach nova, and vice versa.
	fakeServer.Mux.HandleFunc("/os-quota-sets/p1", func(w http.ResponseWriter, r *http.Request) {
		computeCalled = true
		th.TestJSONRequest(t, r, `{"quota_set": {"cores": 64}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(computeQuotaBody))
	})
	volumeServer.Mux.HandleFunc("/os-quota-sets/p1", func(w http.ResponseWriter, r *http.Request) {
		volumeCalled = true
		th.TestJSONRequest(t, r, `{"quota_set": {"gigabytes": 4000}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(volumeQuotaBody))
	})
	networkServer.Mux.HandleFunc("/quotas/p1", func(w http.ResponseWriter, r *http.Request) {
		networkCalled = true
		th.TestJSONRequest(t, r, `{"quota": {"port": 500}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(networkQuotaBody))
	})

	f, fl := setFlagSet(t, []string{"--cores=64", "--gigabytes=4000", "--ports=500"})
	given := f.givenBy(fl)
	if !given.compute || !given.volume || !given.network {
		t.Fatalf("givenBy() = %+v, want all three services", given)
	}

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	s := quotaSession(fakeServer, volumeServer, networkServer)
	if err := runQuotaSet(context.Background(), s, o, "p1", f, fl, given, &buf); err != nil {
		t.Fatalf("runQuotaSet error: %v", err)
	}
	if !computeCalled || !volumeCalled || !networkCalled {
		t.Errorf("fan-out incomplete: compute=%v volume=%v network=%v", computeCalled, volumeCalled, networkCalled)
	}
	out := buf.String()
	for _, want := range []string{"cores", "gigabytes", "ports"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q from the merged result\n---\n%s", want, out)
		}
	}
}

func TestQuotaSet_RequiresAFlag(t *testing.T) {
	cmd := newQuotaSetCommand(nil, &output.Options{Format: output.FormatTable})
	cmd.SetArgs([]string{"p1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("expected a 'nothing to set' error, got %v", err)
	}
}

// A failure after an earlier service already succeeded must name what changed,
// since the three quota APIs share no transaction and cannot be rolled back.
func TestRunQuotaSet_PartialFailureNamesAppliedServices(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	networkServer := th.SetupHTTP()
	defer networkServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-quota-sets/p1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(computeQuotaBody))
	})
	networkServer.Mux.HandleFunc("/quotas/p1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	f, fl := setFlagSet(t, []string{"--cores=64", "--ports=500"})
	given := f.givenBy(fl)

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	s := quotaSession(fakeServer, fakeServer, networkServer)
	err := runQuotaSet(context.Background(), s, o, "p1", f, fl, given, &buf)
	if err == nil {
		t.Fatal("runQuotaSet returned nil on a failing network update")
	}
	for _, want := range []string{"network", "already updated", "compute"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestServiceSelection_DefaultsToAll(t *testing.T) {
	if got := (serviceSelection{}).resolved(); !got.compute || !got.volume || !got.network {
		t.Errorf("empty selection resolved to %+v, want all three", got)
	}
	if got := (serviceSelection{volume: true}).resolved(); got.compute || !got.volume || got.network {
		t.Errorf("volume-only selection resolved to %+v, want volume only", got)
	}
}

// The project ID, not the name, must reach the URL: nova answers an unrecognised
// project with the *default* quotas rather than a 404, so an unresolved name
// would report plausible but wrong numbers. (Carried over from the compute-only
// quota command this package replaced.)
func TestRunQuotaShow_UsesProjectIDInURL(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const projectID = "abcabcabcabc0000abcabcabcabc0000"
	var gotPath string
	fakeServer.Mux.HandleFunc("/os-quota-sets/"+projectID, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"quota_set": {"id": "` + projectID + `", "instances": 10, "cores": 20, "ram": 51200}}`))
	})

	o := &output.Options{Format: output.FormatValue}
	var buf bytes.Buffer
	sel := serviceSelection{compute: true}.resolved()
	if err := runQuotaShow(context.Background(), oneServerSession(fakeServer), o, projectID, false, sel, &buf); err != nil {
		t.Fatalf("runQuotaShow error: %v", err)
	}
	if gotPath != "/os-quota-sets/"+projectID {
		t.Errorf("path = %q, want /os-quota-sets/%s", gotPath, projectID)
	}
	if !strings.Contains(buf.String(), "10") {
		t.Errorf("output missing the instances quota:\n%s", buf.String())
	}
}

// Upstream's --compute / --volume / --network / --all form one mutually exclusive
// group whose default is "all", so `quota show --all` is the explicit spelling of
// the default. It carries no behaviour of its own; what matters is that it exists
// (scripts pass it) and that it cannot be combined with a single-service flag.
func TestQuotaShow_AllFlag(t *testing.T) {
	cmd := newQuotaShowCommand(&auth.Options{}, &output.Options{})
	if cmd.Flags().Lookup("all") == nil {
		t.Fatal("koc quota show: missing --all")
	}
	// Cobra reports group violations from Execute, not from Flags().Parse, so the
	// command is driven the way an operator would.
	cmd.SetArgs([]string{"--all", "--compute"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("--all with --compute should be rejected")
	}
	if !strings.Contains(err.Error(), "all") || !strings.Contains(err.Error(), "compute") {
		t.Errorf("error %q should name the conflicting flags", err.Error())
	}
}
