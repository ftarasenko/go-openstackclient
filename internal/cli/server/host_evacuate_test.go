package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// serveComputeService answers os-services for cmp-1 with the given state.
func serveComputeService(fakeServer th.FakeServer, state string) {
	fakeServer.Mux.HandleFunc("/os-services", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("host") != "cmp-1" {
			_, _ = w.Write([]byte(noServicesBody))
			return
		}
		_, _ = w.Write([]byte(`{"services": [{"id": 7, "binary": "nova-compute", "host": "cmp-1",
		  "state": "` + state + `", "status": "enabled", "zone": "nova"}]}`))
	})
}

func evacuateFlags() *hostEvacuateFlags {
	f := &hostEvacuateFlags{}
	f.hostDrainFlags = *drainFlags()
	return f
}

// TestHostEvacuateRefusesLiveHost is the precheck that matters: nova's
// compute_api.evacuate raises ComputeServiceInUse while the source service is
// up, so without this every server on the host would come back as its own
// identical 409 and the operator would read a table to learn one fact.
func TestHostEvacuateRefusesLiveHost(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveComputeService(fakeServer, "up")
	fakeServer.Mux.HandleFunc("/servers/detail", func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("the drain must refuse before it lists the host's servers")
	})

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	mode, err := evacuateDrainMode(client, evacuateFlags(), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("evacuateDrainMode: %v", err)
	}
	var out, progress bytes.Buffer
	err = runHostDrain(context.Background(), client, o, "cmp-1", drainFlags(), mode, &out, &progress)
	if err == nil {
		t.Fatal("evacuating a host that is still up must be refused")
	}
	for _, want := range []string{"still up", "compute host drain cmp-1", "--cold"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// TestHostEvacuateDownHost is the same check passing: a host nova reports down
// is evacuated, ERROR servers included.
func TestHostEvacuateDownHost(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveComputeService(fakeServer, "down")
	serveServers(fakeServer, hostServersBody, nil)
	rec := newActionRecorder(t, fakeServer, http.StatusOK, idWeb1, idDB1, idWeb2, idCache)

	client := computeClient(fakeServer, "latest")
	o := &output.Options{Format: output.FormatTable}
	mode, err := evacuateDrainMode(client, evacuateFlags(), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("evacuateDrainMode: %v", err)
	}
	var out, progress bytes.Buffer
	if err := runHostDrain(context.Background(), client, o, "cmp-1", drainFlags(), mode, &out, &progress); err != nil {
		t.Fatalf("runHostDrain: %v", err)
	}
	// ACTIVE, SHUTOFF and ERROR are evacuable; PAUSED is not.
	if rec.count() != 3 {
		t.Fatalf("posted %d evacuations, want 3", rec.count())
	}
	if rec.touched(idWeb2) {
		t.Error("the PAUSED server was evacuated; nova does not accept that state")
	}
	if _, ok := rec.body(idCache)["evacuate"]; !ok {
		t.Errorf("the ERROR server was not evacuated: %v", rec.body(idCache))
	}
}

// TestEvacuateBody pins the action body across the microversion boundary nova
// moved --force at.
func TestEvacuateBody(t *testing.T) {
	f := evacuateFlags()
	f.targetHost = "cmp-9"
	f.preserveEphemeral = true

	// Below 2.68 force is a real field.
	var warn bytes.Buffer
	body, err := evacuateBody(computeClientForVersion("2.60"), withForce(f), &warn)
	if err != nil {
		t.Fatalf("evacuateBody: %v", err)
	}
	if body["host"] != "cmp-9" || body["preserve_ephemeral"] != true || body["force"] != true {
		t.Errorf("body at 2.60 = %v", body)
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warning at 2.60: %s", warn.String())
	}

	// At and above 2.68 nova removed it, and sending it fails the whole
	// request — so it is dropped with a warning rather than failing the drain
	// over a flag that no longer exists.
	warn.Reset()
	body, err = evacuateBody(computeClientForVersion("latest"), withForce(f), &warn)
	if err != nil {
		t.Fatalf("evacuateBody: %v", err)
	}
	if _, present := body["force"]; present {
		t.Errorf("force must not be sent at 2.68 or later: %v", body)
	}
	if !strings.Contains(warn.String(), "2.68") {
		t.Errorf("dropping force was not reported: %s", warn.String())
	}

	// adminPass is deliberately absent: one password across a host's worth of
	// servers is not worth offering, and novaclient does not either.
	if _, present := body["adminPass"]; present {
		t.Errorf("evacuate body carries an admin password: %v", body)
	}
}

// computeClientForVersion is a client that only carries a negotiated
// microversion: evacuateBody reads nothing else off it.
func computeClientForVersion(mv string) *gophercloud.ServiceClient {
	return &gophercloud.ServiceClient{Type: "compute", Microversion: mv}
}

func withForce(f *hostEvacuateFlags) *hostEvacuateFlags {
	c := *f
	c.force = true
	return &c
}
