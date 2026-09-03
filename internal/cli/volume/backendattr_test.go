package volume

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Three volumes on two hosts, one of them a decoy whose host name shares a
// prefix with the filter — "storage-1" must not select "storage-10".
const volumeListHostBody = `{
  "volumes": [
    {
      "id": "11111111-1111-1111-1111-111111111111",
      "name": "vol-a", "status": "available", "size": 10,
      "volume_type": "ssd", "bootable": "false", "availability_zone": "nova",
      "os-vol-host-attr:host": "storage-1@vitastor#vitastor-ssd",
      "os-vol-tenant-attr:tenant_id": "proj-9",
      "attachments": []
    },
    {
      "id": "22222222-2222-2222-2222-222222222222",
      "name": "vol-b", "status": "available", "size": 20,
      "volume_type": "ssd", "bootable": "false", "availability_zone": "nova",
      "os-vol-host-attr:host": "storage-10@vitastor#vitastor-ssd",
      "attachments": []
    },
    {
      "id": "33333333-3333-3333-3333-333333333333",
      "name": "vol-c", "status": "available", "size": 30,
      "volume_type": "hdd", "bootable": "false", "availability_zone": "nova",
      "os-vol-host-attr:host": "storage-1@lvm#lvm-1",
      "attachments": []
    }
  ]
}`

// TestVolumeHostMatches pins the boundary rule: a filter matches the whole host
// string or any of its component prefixes, and nothing else. Cinder's host is
// "<host>@<backend>#<pool>", so each separator is a level an operator may
// reasonably stop at — but a bare prefix match would make "storage-1" select
// "storage-10" too, which is the wrong answer in the one case (a drain) the
// filter is for.
func TestVolumeHostMatches(t *testing.T) {
	const host = "storage-1@vitastor#vitastor-ssd"
	for _, want := range []string{host, "storage-1", "storage-1@vitastor"} {
		if !volumeHostMatches(host, want) {
			t.Errorf("volumeHostMatches(%q, %q) = false, want true", host, want)
		}
	}
	for _, want := range []string{"storage-10", "storage", "vitastor", "storage-1@vit", ""} {
		if volumeHostMatches(host, want) {
			t.Errorf("volumeHostMatches(%q, %q) = true, want false", host, want)
		}
	}
	// A volume whose host the caller cannot see (cinder renders the attribute
	// for admins only) matches nothing rather than everything.
	if volumeHostMatches("", "storage-1") {
		t.Error(`volumeHostMatches("", "storage-1") = true, want false`)
	}
}

// TestRunVolumeList_HostColumnAndFilter covers the backend attribution on the
// listing: the Host column under --long, and --host as a client-side filter.
func TestRunVolumeList_HostColumnAndFilter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/volumes/detail", func(w http.ResponseWriter, r *http.Request) {
		// The filter is applied here, not by cinder: its server-side host filter
		// is admin-only and outside the default resource_filters.json allow-list,
		// so sending it would be silently ignored on stock deployments.
		if r.URL.Query().Has("host") {
			t.Errorf("query carries a host filter: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(volumeListHostBody))
	})

	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runVolumeList(context.Background(), volumeClient(fakeServer, "3.70"), o,
		&volumeListFlags{long: true}, "", "", &buf); err != nil {
		t.Fatalf("runVolumeList: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Host", "storage-1@vitastor#vitastor-ssd", "storage-10@vitastor#vitastor-ssd"} {
		if !strings.Contains(out, want) {
			t.Errorf("volume list --long output missing %q\n---\n%s", want, out)
		}
	}

	// --host storage-1 selects the two pools on that host and not storage-10's.
	buf.Reset()
	if err := runVolumeList(context.Background(), volumeClient(fakeServer, "3.70"), o,
		&volumeListFlags{long: true, host: "storage-1"}, "", "", &buf); err != nil {
		t.Fatalf("runVolumeList: %v", err)
	}
	out = buf.String()
	for _, want := range []string{"vol-a", "vol-c"} {
		if !strings.Contains(out, want) {
			t.Errorf("--host storage-1 output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "vol-b") {
		t.Errorf("--host storage-1 matched storage-10's volume\n---\n%s", out)
	}

	// --host composes with --type, and --limit caps what survives both.
	buf.Reset()
	if err := runVolumeList(context.Background(), volumeClient(fakeServer, "3.70"), o,
		&volumeListFlags{host: "storage-1@lvm", volumeType: "hdd"}, "", "", &buf); err != nil {
		t.Fatalf("runVolumeList: %v", err)
	}
	if out = buf.String(); !strings.Contains(out, "vol-c") || strings.Contains(out, "vol-a") {
		t.Errorf("--host with --type = %q, want only vol-c", out)
	}
}

// TestRunVolumeShow_BackendAttribution covers the show view: without
// os-vol-host-attr:host there is no way to tell which pool a volume landed on.
func TestRunVolumeShow_BackendAttribution(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const id = "11111111-1111-1111-1111-111111111111"
	fakeServer.Mux.HandleFunc("/volumes/"+id, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"volume":{"id":"` + id + `","name":"vol-a","status":"available","size":10,
		  "os-vol-host-attr:host":"storage-1@vitastor#vitastor-ssd",
		  "os-vol-tenant-attr:tenant_id":"proj-9"}}`))
	})

	o := &output.Options{Format: output.FormatCSV}
	var buf bytes.Buffer
	if err := runVolumeShow(context.Background(), volumeClient(fakeServer, "3.70"), o, id, &buf); err != nil {
		t.Fatalf("runVolumeShow: %v", err)
	}
	for _, want := range []string{
		"os-vol-host-attr:host", "storage-1@vitastor#vitastor-ssd",
		"os-vol-tenant-attr:tenant_id", "proj-9",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("volume show output missing %q\n---\n%s", want, buf.String())
		}
	}
}

// TestRunVolumeCreate_Wait covers "volume create --wait": cinder answers the
// create with status "creating", so a script that creates and then attaches has
// to see the volume settle. An "error" status must fail the command.
func TestRunVolumeCreate_Wait(t *testing.T) {
	restore := volumePollInterval
	volumePollInterval = time.Millisecond
	defer func() { volumePollInterval = restore }()

	for _, tc := range []struct {
		name    string
		final   string
		wantErr string
	}{
		{name: "becomes available", final: "available"},
		{name: "ends in error", final: "error", wantErr: "entered error status while being created"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			fakeServer.Mux.HandleFunc("/volumes", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"volume":{"id":"new-id","name":"scratch","status":"creating","size":10}}`))
			})
			var gets int
			fakeServer.Mux.HandleFunc("/volumes/new-id", func(w http.ResponseWriter, _ *http.Request) {
				gets++
				status := "downloading"
				if gets > 1 {
					status = tc.final
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"volume":{"id":"new-id","name":"scratch","size":10,"status":"` + status + `"}}`))
			})

			o := &output.Options{Format: output.FormatCSV}
			var buf bytes.Buffer
			f := &volumeCreateFlags{size: 10, wait: true, waitTimeout: 5 * time.Second}
			err := runVolumeCreate(context.Background(), volumeClient(fakeServer, "3.70"), o, "scratch", f, &buf)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("runVolumeCreate: %v", err)
			}
			if gets < 2 {
				t.Errorf("volume was read %d time(s), want the wait to see past downloading", gets)
			}
			// The create response said "creating"; the rendered table must report
			// the settled status instead.
			if out := buf.String(); !strings.Contains(out, "available") || strings.Contains(out, "creating") {
				t.Errorf("output does not report the settled status\n---\n%s", out)
			}
		})
	}
}
