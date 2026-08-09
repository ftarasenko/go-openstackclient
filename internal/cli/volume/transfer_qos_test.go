package volume

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const (
	transferID  = "33333333-3333-3333-3333-333333333333"
	xferVolume  = "44444444-4444-4444-4444-444444444444"
	qosSpecID   = "55555555-5555-5555-5555-555555555555"
	volTypeUUID = "66666666-6666-6666-6666-666666666666"
)

func TestRunTransferCreate_LegacyEndpointCarriesSnapshots(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveVolumeLookup(fakeServer)
	var gotPath string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/os-volume-transfer", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"transfer": {"id": "` + transferID + `", "name": "hand-off",
		  "volume_id": "` + xferVolume + `", "auth_key": "s3cr3t"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "latest")
	if err := runTransferCreate(context.Background(), client, o, xferVolume, "hand-off", false, &out); err != nil {
		t.Fatalf("runTransferCreate returned error: %v", err)
	}

	// The default path stays on the legacy endpoint, which every supported
	// cinder has.
	th.AssertEquals(t, "/os-volume-transfer", gotPath)
	transfer := body["transfer"].(map[string]any)
	th.AssertEquals(t, xferVolume, transfer["volume_id"])
	// The auth key is returned exactly once and is required to accept the
	// transfer, so it has to reach the operator.
	if !strings.Contains(out.String(), "s3cr3t") {
		t.Errorf("output is missing the auth key:\n%s", out.String())
	}
}

func TestRunTransferCreate_NoSnapshotsUsesTheNewerEndpoint(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveVolumeLookup(fakeServer)
	var gotPath, gotMicroversion string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/volume-transfers", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMicroversion = r.Header.Get("OpenStack-API-Version")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"transfer": {"id": "` + transferID + `", "volume_id": "` + xferVolume + `",
		  "auth_key": "s3cr3t"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "latest")
	if err := runTransferCreate(context.Background(), client, o, xferVolume, "", true, &out); err != nil {
		t.Fatalf("runTransferCreate returned error: %v", err)
	}

	// no_snapshots exists only on cinder's newer route, from 3.55; the legacy
	// one has no such field and always takes the snapshots along.
	th.AssertEquals(t, "/volume-transfers", gotPath)
	th.AssertEquals(t, "volume 3.55", gotMicroversion)
	th.AssertEquals(t, true, body["transfer"].(map[string]any)["no_snapshots"])
	// The name is omitted rather than sent empty when the flag was not given.
	if _, present := body["transfer"].(map[string]any)["name"]; present {
		t.Errorf("empty name sent to the API: %#v", body)
	}
}

func TestRunTransferAccept_PostsTheAuthKey(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/os-volume-transfer/"+transferID+"/accept", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "POST", r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"transfer": {"id": "` + transferID + `", "volume_id": "` + xferVolume + `"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "latest")
	if err := runTransferAccept(context.Background(), client, o, transferID, "s3cr3t", &out); err != nil {
		t.Fatalf("runTransferAccept returned error: %v", err)
	}
	th.AssertEquals(t, "s3cr3t", body["accept"].(map[string]any)["auth_key"])
}

func TestRunTransferList_AllProjectsQuery(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotQuery string
	fakeServer.Mux.HandleFunc("/os-volume-transfer/detail", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"transfers": [{"id": "` + transferID + `", "name": "hand-off",
		  "volume_id": "` + xferVolume + `"}]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "latest")
	if err := runTransferList(context.Background(), client, o, true, 0, &out); err != nil {
		t.Fatalf("runTransferList returned error: %v", err)
	}
	th.AssertEquals(t, "all_tenants=true", gotQuery)
	th.AssertEquals(t, transferID+"\thand-off\t"+xferVolume+"\n", out.String())
}

// serveQoS registers the QoS listing and one spec, capturing write bodies.
func serveQoS(t *testing.T, fakeServer th.FakeServer, specs string) (*[]string, *map[string]any) {
	t.Helper()
	var paths []string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/qos-specs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"qos_specs": [{"id": "` + qosSpecID + `", "name": "gold",
		  "consumer": "both", "specs": ` + specs + `}]}`))
	})
	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			// cinder answers an update with just the keys it applied, not the
			// whole spec object.
			_, _ = w.Write([]byte(`{"qos_specs": ` + specs + `}`))
			return
		}
		_, _ = w.Write([]byte(`{"qos_specs": {"id": "` + qosSpecID + `", "name": "gold",
		  "consumer": "both", "specs": ` + specs + `}}`))
	})
	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID+"/delete_keys", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	})
	return &paths, &body
}

func TestRunQoSUnset_DeletesOnlyTheNamedKeys(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	_, body := serveQoS(t, fakeServer, `{"read_iops_sec": "1000", "write_iops_sec": "500"}`)

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "latest")
	if err := runQoSUnset(context.Background(), client, o, qosSpecID, []string{"write_iops_sec"}, &out); err != nil {
		t.Fatalf("runQoSUnset returned error: %v", err)
	}
	th.AssertDeepEquals(t, []any{"write_iops_sec"}, (*body)["keys"])
}

func TestRunQoSSet_NoPropertyClearsExistingKeysFirst(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	paths, body := serveQoS(t, fakeServer, `{"read_iops_sec": "1000", "stale": "yes"}`)

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "latest")
	err := runQoSSet(context.Background(), client, o, qosSpecID, []string{"read_iops_sec=2000"}, true, &out)
	if err != nil {
		t.Fatalf("runQoSSet returned error: %v", err)
	}

	// Cinder's PUT merges keys instead of replacing the map, so --no-property
	// has to delete the survivors explicitly or it would be a no-op.
	var sawDeleteKeys bool
	for _, p := range *paths {
		if strings.Contains(p, "delete_keys") {
			sawDeleteKeys = true
		}
	}
	if !sawDeleteKeys {
		t.Fatalf("--no-property did not clear the existing keys; requests were %v", *paths)
	}
	// A key that --property is about to set again must not be deleted and then
	// re-added: only the ones being dropped are cleared.
	th.AssertDeepEquals(t, []any{"stale"}, (*body)["keys"])
}

func TestRunQoSSet_PropertyOnlyDoesNotClear(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	paths, _ := serveQoS(t, fakeServer, `{"read_iops_sec": "1000"}`)

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "latest")
	err := runQoSSet(context.Background(), client, o, qosSpecID, []string{"write_iops_sec=500"}, false, &out)
	if err != nil {
		t.Fatalf("runQoSSet returned error: %v", err)
	}
	// Without --no-property nothing is deleted; cinder merges the new key into
	// the existing map.
	for _, p := range *paths {
		if strings.Contains(p, "delete_keys") {
			t.Errorf("--property alone must not clear existing keys; requests were %v", *paths)
		}
	}
}

func TestRunQoSDisassociate_AllUsesTheDedicatedEndpoint(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID+"/disassociate_all", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	})

	client := volumeClient(fakeServer, "latest")
	if err := runQoSDisassociate(context.Background(), client, qosSpecID, "", true); err != nil {
		t.Fatalf("runQoSDisassociate returned error: %v", err)
	}
	th.AssertEquals(t, "/qos-specs/"+qosSpecID+"/disassociate_all", gotPath)
}

func TestRunQoSAssociate_SendsVolumeTypeQuery(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveVolumeTypeLookup(fakeServer)
	var gotQuery string
	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID+"/associate", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusAccepted)
	})

	client := volumeClient(fakeServer, "latest")
	if err := runQoSAssociate(context.Background(), client, qosSpecID, volTypeUUID); err != nil {
		t.Fatalf("runQoSAssociate returned error: %v", err)
	}
	th.AssertEquals(t, "vol_type_id="+volTypeUUID, gotQuery)
}

func TestResolveQoSID_AmbiguousNameFails(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/qos-specs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"qos_specs": [
		  {"id": "` + qosSpecID + `", "name": "gold"},
		  {"id": "77777777-7777-7777-7777-777777777777", "name": "gold"}
		]}`))
	})

	client := volumeClient(fakeServer, "latest")
	if _, err := resolveQoSID(context.Background(), client, "gold"); err == nil {
		t.Fatal("expected an ambiguous QoS name to be rejected")
	}
}

// serveVolumeLookup answers the Get-then-list probe resolveVolumeID performs.
func serveVolumeLookup(fakeServer th.FakeServer) {
	fakeServer.Mux.HandleFunc("/volumes/"+xferVolume, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"volume": {"id": "` + xferVolume + `", "name": "data"}}`))
	})
}

// serveVolumeTypeLookup answers the same probe for volume types.
func serveVolumeTypeLookup(fakeServer th.FakeServer) {
	fakeServer.Mux.HandleFunc("/types/"+volTypeUUID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"volume_type": {"id": "` + volTypeUUID + `", "name": "fast"}}`))
	})
}
