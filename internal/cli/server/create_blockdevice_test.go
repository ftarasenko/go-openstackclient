package server

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// TestParseBlockDevice covers the current OSC --block-device key=value form:
// the defaults koc fills in, the types each key is converted to, and the
// combinations nova would reject.
func TestParseBlockDevice(t *testing.T) {
	ok := []struct {
		name string
		in   string
		want map[string]any
	}{
		{
			name: "existing volume, defaults filled in",
			in:   "uuid=vol-1",
			want: map[string]any{"uuid": "vol-1", "source_type": "volume", "destination_type": "volume"},
		},
		{
			name: "blank ephemeral disk defaults to local",
			in:   "source_type=blank,volume_size=20",
			want: map[string]any{"source_type": "blank", "destination_type": "local", "volume_size": 20},
		},
		{
			name: "explicit root device from an image",
			in:   "source_type=image,uuid=img-1,destination_type=volume,volume_size=30,boot_index=0,volume_type=ssd",
			want: map[string]any{
				"source_type": "image", "uuid": "img-1", "destination_type": "volume",
				"volume_size": 30, "boot_index": 0, "volume_type": "ssd",
			},
		},
		{
			name: "hyphenated keys and boolean spellings",
			in:   "uuid=vol-2,delete-on-termination=yes,device-name=/dev/vdc,tag=data",
			want: map[string]any{
				"uuid": "vol-2", "source_type": "volume", "destination_type": "volume",
				"delete_on_termination": true, "device_name": "/dev/vdc", "tag": "data",
			},
		},
		{
			name: "no_device is exempt from the source defaults",
			in:   "device_name=/dev/vdb,no_device=true",
			want: map[string]any{"device_name": "/dev/vdb", "no_device": true},
		},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseBlockDevice(tc.in)
			if err != nil {
				t.Fatalf("parseBlockDevice(%q): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseBlockDevice(%q) =\n%v\nwant\n%v", tc.in, got, tc.want)
			}
		})
	}

	bad := []struct {
		in   string
		want string
	}{
		{"", "expected key=value pairs"},
		{"nope=1", `unknown key "nope"`},
		{"uuid=vol-1,volume_size=big", "volume_size must be an integer"},
		{"uuid=vol-1,delete_on_termination=maybe", "expected true or false"},
		{"source_type=elsewhere,uuid=x", "source_type must be one of"},
		{"uuid=vol-1,destination_type=nowhere", "destination_type must be one of"},
		{"source_type=snapshot", "needs uuid="},
		{"source_type=blank", "needs volume_size="},
	}
	for _, tc := range bad {
		t.Run("reject "+tc.in, func(t *testing.T) {
			_, err := parseBlockDevice(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseBlockDevice(%q) err = %v, want containing %q", tc.in, err, tc.want)
			}
		})
	}
}

// TestParseBlockDeviceMapping covers upstream's legacy positional spelling. The
// resulting entry must never carry boot_index: it is a data disk, and a
// boot_index of 0 would make nova treat it as a second root device.
func TestParseBlockDeviceMapping(t *testing.T) {
	ok := []struct {
		in   string
		want map[string]any
	}{
		{
			in:   "/dev/vdb=vol-1",
			want: map[string]any{"device_name": "/dev/vdb", "uuid": "vol-1", "source_type": "volume", "destination_type": "volume"},
		},
		{
			in: "vdc=snap-1:snapshot:50:true",
			want: map[string]any{
				"device_name": "vdc", "uuid": "snap-1", "source_type": "snapshot",
				"destination_type": "volume", "volume_size": 50, "delete_on_termination": true,
			},
		},
		{
			// The empty type field falls back to volume, and the size is optional.
			in: "vdd=vol-2::",
			want: map[string]any{
				"device_name": "vdd", "uuid": "vol-2", "source_type": "volume",
				"destination_type": "volume",
			},
		},
	}
	for _, tc := range ok {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseBlockDeviceMapping(tc.in)
			if err != nil {
				t.Fatalf("parseBlockDeviceMapping(%q): %v", tc.in, err)
			}
			if _, boot := got["boot_index"]; boot {
				t.Errorf("legacy mapping carries boot_index: %v", got)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseBlockDeviceMapping(%q) =\n%v\nwant\n%v", tc.in, got, tc.want)
			}
		})
	}

	bad := []struct {
		in   string
		want string
	}{
		{"/dev/vdb", "want <dev-name>="},
		{"/dev/vdb=", "missing the volume, snapshot or image id"},
		{"/dev/vdb=vol-1:blank:1", "type must be volume, snapshot or image"},
		{"/dev/vdb=vol-1:volume:big", "size must be an integer"},
		{"/dev/vdb=vol-1:volume:1:sometimes", "expected true or false"},
		{"/dev/vdb=vol-1:volume:1:true:extra", "want <dev-name>="},
	}
	for _, tc := range bad {
		t.Run("reject "+tc.in, func(t *testing.T) {
			_, err := parseBlockDeviceMapping(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseBlockDeviceMapping(%q) err = %v, want containing %q", tc.in, err, tc.want)
			}
		})
	}
}

// TestRunServerCreate_PlacementBlockDevicesAndUserData is the end-to-end body
// assertion for the four create flags added for deterministic placement and
// multi-volume guests: --availability-zone <zone>:<host>, --host, --user-data
// and the two block-device spellings, which must land in one ordered
// block_device_mapping_v2 list.
func TestRunServerCreate_PlacementBlockDevicesAndUserData(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors":[{"id":"2","name":"m1.small"}]}`))
	})
	var gotServer map[string]any
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		gotServer, _ = decodeBody(t, r)["server"].(map[string]any)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","adminPass":"pw"}}`))
	})
	fakeServer.Mux.HandleFunc("/servers/new-id", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","name":"io-writer","status":"ACTIVE"}}`))
	})

	userData := filepath.Join(t.TempDir(), "user-data")
	if err := os.WriteFile(userData, []byte("#cloud-config\npackages: [fio]\n"), 0o600); err != nil {
		t.Fatalf("writing user-data fixture: %v", err)
	}

	f := &serverCreateFlags{
		image:            "img-uuid",
		flavor:           "m1.small",
		availabilityZone: "cpu-epyc:compute-7",
		userData:         userData,
		bdmSpecs:         mustParseBDMs(t, []string{"uuid=vol-1", "source_type=blank,volume_size=100"}, []string{"/dev/vdd=vol-2:volume:10:true"}),
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runServerCreate(context.Background(), computeClient(fakeServer, "2.93"), o, "io-writer", f, &buf); err != nil {
		t.Fatalf("runServerCreate: %v", err)
	}

	if got := gotServer["availability_zone"]; got != "cpu-epyc:compute-7" {
		t.Errorf("availability_zone = %v, want cpu-epyc:compute-7", got)
	}
	// user_data reaches nova base64-encoded.
	if got, want := gotServer["user_data"], "I2Nsb3VkLWNvbmZpZwpwYWNrYWdlczogW2Zpb10K"; got != want {
		t.Errorf("user_data = %v, want %v", got, want)
	}
	// The top-level imageRef stays: no mapping claims boot index 0, so the image
	// is still the root device.
	if got := gotServer["imageRef"]; got != "img-uuid" {
		t.Errorf("imageRef = %v, want img-uuid", got)
	}
	bdms, ok := gotServer["block_device_mapping_v2"].([]any)
	if !ok || len(bdms) != 3 {
		t.Fatalf("block_device_mapping_v2 = %v, want three entries", gotServer["block_device_mapping_v2"])
	}
	first, _ := bdms[0].(map[string]any)
	if first["uuid"] != "vol-1" || first["source_type"] != "volume" || first["destination_type"] != "volume" {
		t.Errorf("first mapping = %v, want the existing volume vol-1", first)
	}
	second, _ := bdms[1].(map[string]any)
	if second["source_type"] != "blank" || second["destination_type"] != "local" || second["volume_size"] != float64(100) {
		t.Errorf("second mapping = %v, want a 100GB blank local disk", second)
	}
	// --block-device-mapping entries follow --block-device ones, in order.
	third, _ := bdms[2].(map[string]any)
	if third["device_name"] != "/dev/vdd" || third["uuid"] != "vol-2" || third["delete_on_termination"] != true {
		t.Errorf("third mapping = %v, want the legacy /dev/vdd entry", third)
	}
}

// mustParseBDMs runs the two flag spellings through their parsers in the order
// RunE does, so a test states the flag values rather than the request maps.
func mustParseBDMs(t *testing.T, blockDevices, mappings []string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range blockDevices {
		bdm, err := parseBlockDevice(raw)
		if err != nil {
			t.Fatalf("parseBlockDevice(%q): %v", raw, err)
		}
		out = append(out, bdm)
	}
	for _, raw := range mappings {
		bdm, err := parseBlockDeviceMapping(raw)
		if err != nil {
			t.Fatalf("parseBlockDeviceMapping(%q): %v", raw, err)
		}
		out = append(out, bdm)
	}
	return out
}

// TestRunServerCreate_HostAndBootIndexZero pins the two remaining body edits:
// nova 2.74's `host`, which servers.CreateOpts cannot express, and dropping the
// top-level imageRef once a mapping claims boot index 0 — nova rejects the pair
// as two root devices.
func TestRunServerCreate_HostAndBootIndexZero(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors":[{"id":"2","name":"m1.small"}]}`))
	})
	var gotServer map[string]any
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		gotServer, _ = decodeBody(t, r)["server"].(map[string]any)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","adminPass":"pw"}}`))
	})
	fakeServer.Mux.HandleFunc("/servers/new-id", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","name":"pinned","status":"ACTIVE"}}`))
	})

	f := &serverCreateFlags{
		image:              "img-uuid",
		flavor:             "m1.small",
		host:               "compute-7",
		hypervisorHostname: "compute-7.example.com",
		bdmSpecs: mustParseBDMs(t,
			[]string{"source_type=image,uuid=img-uuid,volume_size=40,boot_index=0"}, nil),
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runServerCreate(context.Background(), computeClient(fakeServer, "2.93"), o, "pinned", f, &buf); err != nil {
		t.Fatalf("runServerCreate: %v", err)
	}
	if got := gotServer["host"]; got != "compute-7" {
		t.Errorf("host = %v, want compute-7", got)
	}
	if got := gotServer["hypervisor_hostname"]; got != "compute-7.example.com" {
		t.Errorf("hypervisor_hostname = %v, want compute-7.example.com", got)
	}
	if v, ok := gotServer["imageRef"]; ok && v != "" {
		t.Errorf("imageRef = %v, want empty once a mapping claims boot_index 0", v)
	}
}

// TestValidateServerCreate_HostConflict covers the one combination nova refuses
// outright: a zone that already names a host, plus the 2.74 host flags.
func TestValidateServerCreate_HostConflict(t *testing.T) {
	cases := []*serverCreateFlags{
		{flavor: "m1.small", availabilityZone: "cpu-epyc:compute-7", host: "compute-8"},
		{flavor: "m1.small", availabilityZone: "cpu-epyc:compute-7", hypervisorHostname: "compute-8"},
	}
	for _, f := range cases {
		if err := validateServerCreate(f); err == nil ||
			!strings.Contains(err.Error(), "cannot be combined with an --availability-zone") {
			t.Errorf("validateServerCreate(%+v) err = %v, want the host-conflict rejection", f, err)
		}
	}
	// A zone on its own is fine, and so are the 2.74 flags on their own.
	for _, f := range []*serverCreateFlags{
		{flavor: "m1.small", availabilityZone: "cpu-epyc"},
		{flavor: "m1.small", host: "compute-7", hypervisorHostname: "compute-7.example.com"},
	} {
		if err := validateServerCreate(f); err != nil {
			t.Errorf("validateServerCreate(%+v) = %v, want nil", f, err)
		}
	}
}

// TestReadUserData covers the two ways the flag can be wrong. An empty file is
// rejected rather than sent: nova accepts it and the guest then boots with no
// cloud-init payload at all, which is the failure this flag exists to avoid.
func TestReadUserData(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	if _, err := readUserData(empty); err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Errorf("readUserData(empty) err = %v, want the empty-file rejection", err)
	}
	if _, err := readUserData(filepath.Join(dir, "absent")); err == nil ||
		!strings.Contains(err.Error(), "reading --user-data") {
		t.Errorf("readUserData(absent) err = %v, want a read failure", err)
	}
}

// TestRunServerCreate_Wait asserts --wait polls until nova reports ACTIVE and
// renders the settled status, and that it fails the command when the build ends
// in ERROR — the case a caller without --wait discovers much later.
func TestRunServerCreate_Wait(t *testing.T) {
	restore := statusPollInterval
	statusPollInterval = time.Millisecond
	defer func() { statusPollInterval = restore }()

	for _, tc := range []struct {
		name    string
		final   string
		wantErr string
	}{
		{name: "reaches ACTIVE", final: "ACTIVE"},
		{name: "ends in ERROR", final: "ERROR", wantErr: "ERROR status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"flavors":[{"id":"2","name":"m1.small"}]}`))
			})
			fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"server":{"id":"new-id","adminPass":"pw"}}`))
			})
			var gets int
			fakeServer.Mux.HandleFunc("/servers/new-id", func(w http.ResponseWriter, _ *http.Request) {
				gets++
				status := "BUILD"
				if gets > 1 {
					status = tc.final
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"server":{"id":"new-id","name":"waited","status":"` + status + `"}}`))
			})

			f := &serverCreateFlags{flavor: "m1.small", image: "img", wait: true, waitTimeout: 5 * time.Second}
			o := &output.Options{Format: output.FormatTable}
			var buf bytes.Buffer
			err := runServerCreate(context.Background(), computeClient(fakeServer, "2.93"), o, "waited", f, &buf)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("runServerCreate: %v", err)
			}
			if gets < 2 {
				t.Errorf("server was polled %d time(s), want the wait to see past BUILD", gets)
			}
			if !strings.Contains(buf.String(), "ACTIVE") {
				t.Errorf("output does not report the settled status\n---\n%s", buf.String())
			}
		})
	}
}

// TestServerCreate_FlagParity pins the "server create" option surface against
// upstream OSC's parser (openstackclient/compute/v2/server.py CreateServer).
// The four added here are the ones a deterministic-placement workload cannot do
// without: a host to land on, more than one disk in a single call, a cloud-init
// payload, and a wait so a build loop does not hand-roll polling.
func TestServerCreate_FlagParity(t *testing.T) {
	root := NewCommand(&auth.Options{}, &output.Options{})
	leaf, _, err := root.Find([]string{"create"})
	if err != nil || leaf == nil {
		t.Fatalf("server create: not found: %v", err)
	}
	for _, name := range []string{
		"availability-zone", "host", "hypervisor-hostname",
		"user-data", "block-device", "block-device-mapping",
		"wait", flagWaitTimeout,
	} {
		if leaf.Flags().Lookup(name) == nil {
			t.Errorf("koc server create: missing --%s", name)
		}
	}
}
