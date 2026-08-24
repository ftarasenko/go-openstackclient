package volume

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
)

// staleProperties is the survivor computation behind --no-property: cinder's PUT
// merges keys, so the ones that must go have to be named explicitly.
func TestStaleProperties(t *testing.T) {
	tests := []struct {
		name    string
		current map[string]string
		keep    map[string]string
		want    []string
	}{
		{
			name:    "everything goes when nothing is being re-set",
			current: map[string]string{"read_iops_sec": "1000", "write_iops_sec": "500"},
			want:    []string{"read_iops_sec", "write_iops_sec"},
		},
		{
			// A key that --property is about to set is left alone, so it is never
			// briefly absent and a failed second call cannot empty the spec.
			name:    "a key that is about to be re-set is left alone",
			current: map[string]string{"read_iops_sec": "1000", "write_iops_sec": "500"},
			keep:    map[string]string{"read_iops_sec": "2000"},
			want:    []string{"write_iops_sec"},
		},
		{
			name:    "nothing to delete when every key is being re-set",
			current: map[string]string{"read_iops_sec": "1000"},
			keep:    map[string]string{"read_iops_sec": "2000"},
		},
		{
			// A brand-new key in --property has nothing to clear.
			name: "an empty specification yields nothing",
			keep: map[string]string{"consumer": "front-end"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := staleProperties(tt.current, tt.keep)
			// Map iteration order is unspecified, so compare as a set.
			slices.Sort(got)
			want := slices.Clone(tt.want)
			slices.Sort(want)
			if len(got) != len(want) || (len(got) > 0 && !slices.Equal(got, want)) {
				t.Errorf("staleProperties() = %v, want %v", got, want)
			}
		})
	}
}

// clearOtherProperties reads the current specification and deletes exactly the
// keys that survive the --property set.
func TestClearOtherProperties_DeletesOnlyTheStaleKeys(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var deleted []string
	var sawDelete bool
	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"qos_specs": {"id": "` + qosSpecID + `", "name": "gold",
		  "consumer": "front-end", "specs": {"read_iops_sec": "1000", "write_iops_sec": "500"}}}`))
	})
	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID+"/delete_keys", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPut)
		sawDelete = true
		var body struct {
			Keys []string `json:"keys"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		deleted = body.Keys
		w.WriteHeader(http.StatusAccepted)
	})

	client := volumeClient(fakeServer, "latest")
	keep := map[string]string{"read_iops_sec": "2000"}
	if err := clearOtherProperties(context.Background(), client, qosSpecID, "gold", keep); err != nil {
		t.Fatalf("clearOtherProperties() error = %v", err)
	}
	if !sawDelete {
		t.Fatal("clearOtherProperties() issued no delete_keys call")
	}
	if len(deleted) != 1 || deleted[0] != "write_iops_sec" {
		t.Errorf("delete_keys = %v, want only the key that is not being re-set", deleted)
	}
}

// Nothing stale means no delete_keys request at all.
func TestClearOtherProperties_NoStaleKeysIssuesNoDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"qos_specs": {"id": "` + qosSpecID + `", "name": "gold",
		  "consumer": "front-end", "specs": {"read_iops_sec": "1000"}}}`))
	})
	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID+"/delete_keys", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("delete_keys called although every key is being re-set")
		w.WriteHeader(http.StatusAccepted)
	})

	keep := map[string]string{"read_iops_sec": "2000"}
	if err := clearOtherProperties(context.Background(), volumeClient(fakeServer, "latest"), qosSpecID, "gold", keep); err != nil {
		t.Fatalf("clearOtherProperties() error = %v", err)
	}
}

// A failed read must say what it was doing, naming the spec as the user typed it.
func TestClearOtherProperties_ReadFailure(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/qos-specs/"+qosSpecID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	err := clearOtherProperties(context.Background(), volumeClient(fakeServer, "latest"), qosSpecID, "gold", nil)
	if err == nil || !strings.Contains(err.Error(), `reading QoS specification "gold" before clearing it`) {
		t.Fatalf("clearOtherProperties() error = %v, want the reading-before-clearing message", err)
	}
}
