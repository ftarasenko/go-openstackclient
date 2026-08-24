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

// Additional synthetic IDs for this file. The other IDs used below
// (qosSpecID, volTypeUUID, transferID, xferVolume, writeBackupID) are declared
// in transfer_qos_test.go / snapshot_backup_write_test.go in this same
// package.
const (
	qosSpecID2   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	qosSpecID3   = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	extendVolID  = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	unknownVType = "no-such-type"
)

// --- runQoSList --------------------------------------------------------

func TestRunQoSList_RendersRowsAndRespectsLimit(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/qos-specs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"qos_specs": [
		  {"id": "` + qosSpecID + `", "name": "gold", "consumer": "both", "specs": {}},
		  {"id": "` + qosSpecID2 + `", "name": "silver", "consumer": "both", "specs": {}},
		  {"id": "` + qosSpecID3 + `", "name": "bronze", "consumer": "both", "specs": {}}
		]}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "json"}
	client := volumeClient(fakeServer, "3.59")
	if err := runQoSList(context.Background(), client, o, 2, &out); err != nil {
		t.Fatalf("runQoSList returned error: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("decoding output: %v; output was %s", err, out.String())
	}
	// The API only treats limit as a page size; koc enforces it as a hard cap.
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (limit not enforced): %s", len(rows), out.String())
	}
	th.AssertEquals(t, "gold", rows[0]["Name"])
}

func TestRunQoSList_ErrorPropagates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/qos-specs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "3.59")
	err := runQoSList(context.Background(), client, o, 0, &out)
	if err == nil {
		t.Fatal("expected an error from a failing list request")
	}
	if !strings.Contains(err.Error(), "listing volume QoS specifications") {
		t.Errorf("error = %q, want it to name the failing operation", err.Error())
	}
}

// --- runQoSCreate -------------------------------------------------------

func TestRunQoSCreate_SendsConsumerAndProperties(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/qos-specs", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "POST", r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"qos_specs": {"id": "` + qosSpecID + `", "name": "gold",
		  "consumer": "front-end", "specs": {"read_iops_sec": "100"}}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "3.59")
	err := runQoSCreate(context.Background(), client, o, "gold", "front-end", []string{"read_iops_sec=100"}, &out)
	if err != nil {
		t.Fatalf("runQoSCreate returned error: %v", err)
	}

	spec := body["qos_specs"].(map[string]any)
	th.AssertEquals(t, "gold", spec["name"])
	th.AssertEquals(t, "front-end", spec["consumer"])
	th.AssertEquals(t, "100", spec["read_iops_sec"])
	if !strings.Contains(out.String(), qosSpecID) {
		t.Errorf("output is missing the created spec's ID:\n%s", out.String())
	}
}

func TestRunQoSCreate_InvalidPropertyIsRejectedBeforeAnyRequest(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	// No /qos-specs handler: an invalid --property must fail before any
	// request is issued.

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "3.59")
	err := runQoSCreate(context.Background(), client, o, "gold", "", []string{"noequals"}, &out)
	if err == nil {
		t.Fatal("expected an error for a malformed --property")
	}
	if !strings.Contains(err.Error(), "parsing --property") {
		t.Errorf("error = %q, want it to name --property", err.Error())
	}
}

func TestRunQoSCreate_ServerErrorPropagates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/qos-specs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "3.59")
	err := runQoSCreate(context.Background(), client, o, "gold", "", nil, &out)
	if err == nil {
		t.Fatal("expected an error from a failing create request")
	}
	if !strings.Contains(err.Error(), `creating volume QoS specification "gold"`) {
		t.Errorf("error = %q, want it to name the spec", err.Error())
	}
}

// --- runQoSDelete -------------------------------------------------------

func TestRunQoSDelete_ForceFlagSetsQuery(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotQuery string
	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusAccepted)
	})

	client := volumeClient(fakeServer, "3.59")
	if err := runQoSDelete(context.Background(), client, []string{qosSpecID}, true); err != nil {
		t.Fatalf("runQoSDelete returned error: %v", err)
	}
	th.AssertEquals(t, "DELETE", gotMethod)
	th.AssertEquals(t, "force=true", gotQuery)
}

func TestRunQoSDelete_ErrorIsNamedPerRef(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	client := volumeClient(fakeServer, "3.59")
	err := runQoSDelete(context.Background(), client, []string{qosSpecID}, false)
	if err == nil {
		t.Fatal("expected an error from a failing delete request")
	}
	if !strings.Contains(err.Error(), "deleting volume QoS specification") {
		t.Errorf("error = %q, want it to name the failing operation", err.Error())
	}
}

// --- runTransferShow / runTransferDelete --------------------------------

func TestRunTransferShow_RendersFields(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-volume-transfer/"+transferID, func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "GET", r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"transfer": {"id": "` + transferID + `", "name": "hand-off",
		  "volume_id": "` + xferVolume + `", "auth_key": "s3cr3t"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "3.59")
	if err := runTransferShow(context.Background(), client, o, transferID, &out); err != nil {
		t.Fatalf("runTransferShow returned error: %v", err)
	}
	if !strings.Contains(out.String(), transferID) || !strings.Contains(out.String(), xferVolume) {
		t.Errorf("output is missing expected fields:\n%s", out.String())
	}
}

func TestRunTransferShow_ErrorPropagates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-volume-transfer/"+transferID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "3.59")
	err := runTransferShow(context.Background(), client, o, transferID, &out)
	if err == nil {
		t.Fatal("expected an error from a missing transfer request")
	}
	if !strings.Contains(err.Error(), "showing volume transfer request "+transferID) {
		t.Errorf("error = %q, want it to name the transfer request", err.Error())
	}
}

func TestRunTransferDelete_DeletesEachRef(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/os-volume-transfer/"+transferID, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusAccepted)
	})

	client := volumeClient(fakeServer, "3.59")
	if err := runTransferDelete(context.Background(), client, []string{transferID}); err != nil {
		t.Fatalf("runTransferDelete returned error: %v", err)
	}
	th.AssertEquals(t, "DELETE", gotMethod)
}

func TestRunTransferDelete_ErrorPropagates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-volume-transfer/"+transferID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	client := volumeClient(fakeServer, "3.59")
	err := runTransferDelete(context.Background(), client, []string{transferID})
	if err == nil {
		t.Fatal("expected an error from a failing delete request")
	}
	if !strings.Contains(err.Error(), "deleting volume transfer request "+transferID) {
		t.Errorf("error = %q, want it to name the transfer request", err.Error())
	}
}

// --- runBackupUnset ------------------------------------------------------

func TestRunBackupUnset_RemovesOnlyTheNamedKeys(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/backups/"+writeBackupID, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding request body: %v", err)
			}
			_, _ = w.Write([]byte(`{"backup": {"id": "` + writeBackupID + `"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"backup": {"id": "` + writeBackupID + `", "name": "backup-1",
		  "metadata": {"keep": "yes", "drop": "me"}}}`))
	})

	client := volumeClient(fakeServer, "3.59")
	if err := runBackupUnset(context.Background(), client, writeBackupID, []string{"drop"}); err != nil {
		t.Fatalf("runBackupUnset returned error: %v", err)
	}

	metadata := body["backup"].(map[string]any)["metadata"].(map[string]any)
	th.AssertEquals(t, 1, len(metadata))
	th.AssertEquals(t, "yes", metadata["keep"])
}

func TestRunBackupUnset_ReadErrorPropagates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// The first GET (resolveBackupID's direct-get probe) succeeds so the
	// literal-ID path is taken; the second GET (mergedBackupMetadata reading
	// the current properties) fails.
	var calls int
	fakeServer.Mux.HandleFunc("/backups/"+writeBackupID, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"backup": {"id": "` + writeBackupID + `"}}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})

	client := volumeClient(fakeServer, "3.59")
	err := runBackupUnset(context.Background(), client, writeBackupID, []string{"drop"})
	if err == nil {
		t.Fatal("expected an error when reading current metadata fails")
	}
	if !strings.Contains(err.Error(), "reading backup") {
		t.Errorf("error = %q, want it to name the read step", err.Error())
	}
}

func TestRunBackupUnset_UpdateErrorPropagates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/backups/"+writeBackupID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"backup": {"id": "` + writeBackupID + `", "metadata": {"keep": "yes"}}}`))
	})

	client := volumeClient(fakeServer, "3.59")
	err := runBackupUnset(context.Background(), client, writeBackupID, []string{"keep"})
	if err == nil {
		t.Fatal("expected an error from a failing update request")
	}
	if !strings.Contains(err.Error(), `removing properties from backup "`+writeBackupID+`"`) {
		t.Errorf("error = %q, want it to name the backup", err.Error())
	}
}

// --- runVolumeExtend -----------------------------------------------------

func TestRunVolumeExtend_ExtendsAndReportsResultingVolume(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var getCalls int
	fakeServer.Mux.HandleFunc("/volumes/"+extendVolID, func(w http.ResponseWriter, _ *http.Request) {
		getCalls++
		w.Header().Set("Content-Type", "application/json")
		if getCalls == 1 {
			_, _ = w.Write([]byte(`{"volume": {"id": "` + extendVolID + `", "name": "data", "size": 10}}`))
			return
		}
		_, _ = w.Write([]byte(`{"volume": {"id": "` + extendVolID + `", "name": "data", "size": 20}}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/volumes/"+extendVolID+"/action", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "POST", r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "3.59")
	err := runVolumeExtend(context.Background(), client, o, extendVolID, 20, &out)
	if err != nil {
		t.Fatalf("runVolumeExtend returned error: %v", err)
	}

	extend := body["os-extend"].(map[string]any)
	th.AssertEquals(t, float64(20), extend["new_size"])
	// The volume is re-fetched after the async extend so the reported status
	// reflects what cinder actually did, not just what was asked for.
	if getCalls != 2 {
		t.Errorf("got %d GET /volumes/<id> calls, want 2 (resolve + post-extend fetch)", getCalls)
	}
	if !strings.Contains(out.String(), "20") {
		t.Errorf("output does not report the extended size:\n%s", out.String())
	}
}

func TestRunVolumeExtend_RejectsNonPositiveSize(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	// No handlers registered: a bad size must be rejected before any request.

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "3.59")
	err := runVolumeExtend(context.Background(), client, o, extendVolID, 0, &out)
	if err == nil {
		t.Fatal("expected an error for a non-positive size")
	}
	if !strings.Contains(err.Error(), "positive number of GiB") {
		t.Errorf("error = %q, want it to explain the size requirement", err.Error())
	}
}

func TestRunVolumeExtend_ExtendRequestErrorPropagates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/volumes/"+extendVolID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"volume": {"id": "` + extendVolID + `", "size": 10}}`))
	})
	fakeServer.Mux.HandleFunc("/volumes/"+extendVolID+"/action", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "3.59")
	err := runVolumeExtend(context.Background(), client, o, extendVolID, 20, &out)
	if err == nil {
		t.Fatal("expected an error from a failing extend request")
	}
	if !strings.Contains(err.Error(), "extending volume") {
		t.Errorf("error = %q, want it to name the extend step", err.Error())
	}
}

func TestRunVolumeExtend_PostExtendFetchErrorPropagates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var getCalls int
	fakeServer.Mux.HandleFunc("/volumes/"+extendVolID, func(w http.ResponseWriter, _ *http.Request) {
		getCalls++
		if getCalls == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"volume": {"id": "` + extendVolID + `", "size": 10}}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	fakeServer.Mux.HandleFunc("/volumes/"+extendVolID+"/action", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	client := volumeClient(fakeServer, "3.59")
	err := runVolumeExtend(context.Background(), client, o, extendVolID, 20, &out)
	if err == nil {
		t.Fatal("expected an error when the post-extend fetch fails")
	}
	if !strings.Contains(err.Error(), "getting volume") {
		t.Errorf("error = %q, want it to name the post-extend fetch", err.Error())
	}
}

// --- runQoSDisassociate: finish covering its branches ---------------------

func TestRunQoSDisassociate_NonAllResolvesTypeAndSendsQuery(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveVolumeTypeLookup(fakeServer)
	var gotQuery string
	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID+"/disassociate", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusAccepted)
	})

	client := volumeClient(fakeServer, "3.59")
	err := runQoSDisassociate(context.Background(), client, qosSpecID, volTypeUUID, false)
	if err != nil {
		t.Fatalf("runQoSDisassociate returned error: %v", err)
	}
	th.AssertEquals(t, "vol_type_id="+volTypeUUID, gotQuery)
}

func TestRunQoSDisassociate_TypeResolutionErrorStopsBeforeRequest(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	// No /types handler and no /disassociate handler: an unresolvable volume
	// type must fail before any disassociate request is attempted.

	client := volumeClient(fakeServer, "3.59")
	err := runQoSDisassociate(context.Background(), client, qosSpecID, unknownVType, false)
	if err == nil {
		t.Fatal("expected an error for an unresolvable volume type")
	}
	if !strings.Contains(err.Error(), "looking up volume type") {
		t.Errorf("error = %q, want it to name the volume-type lookup", err.Error())
	}
}

func TestRunQoSDisassociate_QoSResolutionErrorStopsBeforeAllCheck(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/qos-specs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"qos_specs": [
		  {"id": "` + qosSpecID + `", "name": "gold"},
		  {"id": "` + qosSpecID2 + `", "name": "gold"}
		]}`))
	})
	var disassociateAllCalled bool
	fakeServer.Mux.HandleFunc("/qos-specs/gold/disassociate_all", func(w http.ResponseWriter, _ *http.Request) {
		disassociateAllCalled = true
		w.WriteHeader(http.StatusAccepted)
	})

	client := volumeClient(fakeServer, "3.59")
	err := runQoSDisassociate(context.Background(), client, "gold", "", true)
	if err == nil {
		t.Fatal("expected an error for an ambiguous QoS name")
	}
	if disassociateAllCalled {
		t.Error("disassociate_all must not be called once QoS resolution fails")
	}
}

func TestRunQoSDisassociate_DisassociateErrorPropagates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	serveVolumeTypeLookup(fakeServer)
	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID+"/disassociate", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	client := volumeClient(fakeServer, "3.59")
	err := runQoSDisassociate(context.Background(), client, qosSpecID, volTypeUUID, false)
	if err == nil {
		t.Fatal("expected an error from a failing disassociate request")
	}
	if !strings.Contains(err.Error(), "disassociating volume type") {
		t.Errorf("error = %q, want it to name the disassociate step", err.Error())
	}
}

func TestRunQoSDisassociate_AllErrorPropagates(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID+"/disassociate_all", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	client := volumeClient(fakeServer, "3.59")
	err := runQoSDisassociate(context.Background(), client, qosSpecID, "", true)
	if err == nil {
		t.Fatal("expected an error from a failing disassociate-all request")
	}
	if !strings.Contains(err.Error(), "disassociating every volume type") {
		t.Errorf("error = %q, want it to name the disassociate-all step", err.Error())
	}
}
