package volume

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Filtering by another project or user is a cross-project read, which cinder
// honors only together with all_tenants — so either flag must imply it rather
// than quietly returning an empty list.
func TestRunVolumeList_ProjectAndUserImplyAllTenants(t *testing.T) {
	tests := []struct {
		name      string
		flags     volumeListFlags
		projectID string
		userID    string
		wantQuery map[string]string
	}{
		{
			name:      "no scope flags",
			wantQuery: map[string]string{},
		},
		{
			name:      "project implies all_tenants",
			projectID: "p1",
			wantQuery: map[string]string{"all_tenants": "true", "project_id": "p1"},
		},
		{
			name:      "user implies all_tenants",
			userID:    "u1",
			wantQuery: map[string]string{"all_tenants": "true", "user_id": "u1"},
		},
		{
			name:      "both",
			projectID: "p1",
			userID:    "u1",
			wantQuery: map[string]string{"all_tenants": "true", "project_id": "p1", "user_id": "u1"},
		},
		{
			name:      "explicit --all-projects alone",
			flags:     volumeListFlags{allProjects: true},
			wantQuery: map[string]string{"all_tenants": "true"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			fakeServer.Mux.HandleFunc("/volumes/detail", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodGet)
				got := r.URL.Query()
				for key, want := range tc.wantQuery {
					if got.Get(key) != want {
						t.Errorf("query %s = %q, want %q (full query %q)", key, got.Get(key), want, r.URL.RawQuery)
					}
				}
				if len(tc.wantQuery) == 0 {
					for _, key := range []string{"all_tenants", "project_id", "user_id"} {
						if got.Has(key) {
							t.Errorf("query should not carry %s, got %q", key, r.URL.RawQuery)
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"volumes": [{"id": "v1", "name": "data", "status": "available", "size": 10}]}`))
			})

			o := &output.Options{Format: output.FormatTable}
			var buf bytes.Buffer
			flags := tc.flags
			err := runVolumeList(context.Background(), volumeClient(fakeServer, "latest"), o, &flags, tc.projectID, tc.userID, &buf)
			if err != nil {
				t.Fatalf("runVolumeList error: %v", err)
			}
			if !strings.Contains(buf.String(), "v1") {
				t.Errorf("output missing the volume\n---\n%s", buf.String())
			}
		})
	}
}

func TestRunVolumeCreate_FromBackup(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/volumes", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		// No size: cinder derives it from the backup, so --size is not required.
		th.TestJSONRequest(t, r, `{"volume": {"name": "restored", "backup_id": "b1"}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"volume": {"id": "v9", "name": "restored", "status": "creating", "size": 10}}`))
	})

	o := &output.Options{Format: output.FormatTable}
	f := &volumeCreateFlags{backup: "b1"}
	var buf bytes.Buffer
	if err := runVolumeCreate(context.Background(), volumeClient(fakeServer, "latest"), o, "restored", f, &buf); err != nil {
		t.Fatalf("runVolumeCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(buf.String(), "v9") {
		t.Errorf("output missing the new volume ID\n---\n%s", buf.String())
	}
}

// --size stays required when there is no source to derive it from.
func TestRunVolumeCreate_SizeRequiredWithoutASource(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runVolumeCreate(context.Background(), volumeClient(fakeServer, "latest"), o, "blank", &volumeCreateFlags{}, &buf)
	if err == nil || !strings.Contains(err.Error(), "--size") {
		t.Fatalf("expected a --size error, got %v", err)
	}
}
