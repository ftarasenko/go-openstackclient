package network

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"
	"github.com/spf13/pflag"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// changedSet is a minimal stand-in for the "which flags were given" interface the
// sparse-update seams take, so a test can express intent without building a
// full cobra command.
type changedSet map[string]bool

func (c changedSet) Changed(name string) bool { return c[name] }

const subnetPoolListBody = `{"subnetpools": [
  {
    "id": "sp1", "name": "shared-v4", "project_id": "p1",
    "prefixes": ["10.0.0.0/8"], "default_prefixlen": "24", "min_prefixlen": "8",
    "max_prefixlen": "32", "default_quota": 0, "address_scope_id": "as1",
    "ip_version": 4, "shared": true, "is_default": true, "description": "main pool"
  },
  {
    "id": "sp2", "name": "tenant-v6", "project_id": "p2",
    "prefixes": ["fd00::/48"], "default_prefixlen": "64", "min_prefixlen": "64",
    "max_prefixlen": "128", "ip_version": 6, "shared": false, "is_default": false
  }
]}`

func TestRunSubnetPoolList_FiltersAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/subnetpools", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		th.TestFormValues(t, r, map[string]string{
			"ip_version": "4",
			"shared":     "true",
			"project_id": "p1",
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(subnetPoolListBody))
	})

	o := &output.Options{Format: output.FormatTable}
	shared := true
	f := &subnetPoolListFlags{ipVersion: 4, shared: &shared}

	var buf bytes.Buffer
	if err := runSubnetPoolList(context.Background(), networkClient(fakeServer), o, f, "p1", &buf); err != nil {
		t.Fatalf("runSubnetPoolList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{"ID", "Name", "Prefixes", "sp1", "shared-v4", "10.0.0.0/8", "fd00::/48"} {
		if !strings.Contains(out, want) {
			t.Errorf("subnet pool list output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "Address Scope") {
		t.Errorf("default output should not carry --long columns:\n%s", out)
	}
}

func TestRunSubnetPoolCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/subnetpools", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"subnetpool": {
          "name": "new-pool",
          "prefixes": ["192.168.0.0/16"],
          "default_prefixlen": 24,
          "shared": true,
          "project_id": "p1"
        }}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"subnetpool": {
          "id": "sp9", "name": "new-pool", "prefixes": ["192.168.0.0/16"],
          "default_prefixlen": "24", "min_prefixlen": "8", "max_prefixlen": "32",
          "ip_version": 4, "shared": true
        }}`))
	})

	o := &output.Options{Format: output.FormatTable}
	// isDefault=false is deliberately absent from the expected body above:
	// CreateOpts tags IsDefault omitempty, so a false is dropped — which matches
	// neutron's own default, so --no-default still lands on the right state.
	shared, isDefault := true, false
	f := &subnetPoolWriteFlags{
		prefixes:         []string{"192.168.0.0/16"},
		defaultPrefixLen: 24,
		shared:           &shared,
		isDefault:        &isDefault,
	}

	var buf bytes.Buffer
	if err := runSubnetPoolCreate(context.Background(), networkClient(fakeServer), o, "new-pool", f, "p1", &buf); err != nil {
		t.Fatalf("runSubnetPoolCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(buf.String(), "sp9") {
		t.Errorf("output missing the new pool ID\n---\n%s", buf.String())
	}
}

// A "set" must send only the attributes whose flags were given, so an unrelated
// --description cannot silently reset prefixes or the quota.
func TestRunSubnetPoolSet_OnlySendsGivenAttributes(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// resolveSubnetPoolID name-filters first; an empty result falls back to the
	// literal reference.
	fakeServer.Mux.HandleFunc("/subnetpools", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"subnetpools": []}`))
	})
	var gotMethod string
	fakeServer.Mux.HandleFunc("/subnetpools/sp1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"subnetpool": {"description": "new text"}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"subnetpool": {
          "id": "sp1", "name": "shared-v4", "prefixes": ["10.0.0.0/8"],
          "default_prefixlen": "24", "min_prefixlen": "8", "max_prefixlen": "32",
          "ip_version": 4, "description": "new text"
        }}`))
	})

	o := &output.Options{Format: output.FormatTable}
	// prefixes/quota are populated but their flags were NOT given, so they must
	// not appear in the request body.
	f := &subnetPoolWriteFlags{
		description:  "new text",
		prefixes:     []string{"172.16.0.0/12"},
		defaultQuota: 99,
	}

	var buf bytes.Buffer
	err := runSubnetPoolSet(context.Background(), networkClient(fakeServer), o, "sp1", f,
		changedSet{"description": true}, &buf)
	if err != nil {
		t.Fatalf("runSubnetPoolSet error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
}

// A set with no attribute flags is rejected before any request is made, so a
// no-op invocation costs no round trip. The fake server therefore registers
// nothing: any HTTP call at all fails the test with a 404.
func TestRunSubnetPoolSet_RejectsEmptyUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runSubnetPoolSet(context.Background(), networkClient(fakeServer), o, "sp1",
		&subnetPoolWriteFlags{}, changedSet{}, &buf)
	if err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("expected a 'nothing to set' error, got %v", err)
	}
}

// --share is registered on create only: neutron's subnetpool PUT has no "shared"
// attribute, so offering it on set would silently do nothing.
func TestSubnetPoolSet_HasNoShareFlag(t *testing.T) {
	set := newSubnetPoolSetCommand(nil, &output.Options{})
	if set.Flags().Lookup("share") != nil {
		t.Error(`"subnet pool set" must not define --share: the neutron PUT ignores it`)
	}
	create := newSubnetPoolCreateCommand(nil, &output.Options{})
	if create.Flags().Lookup("share") == nil {
		t.Error(`"subnet pool create" should define --share`)
	}
}

const trunkListBody = `{"trunks": [
  {
    "id": "t1", "name": "trunk-a", "port_id": "parent-1", "status": "ACTIVE",
    "admin_state_up": true, "project_id": "p1", "description": "vm-a trunk",
    "sub_ports": [
      {"port_id": "sub-1", "segmentation_type": "vlan", "segmentation_id": 101},
      {"port_id": "sub-2", "segmentation_type": "vlan", "segmentation_id": 102}
    ]
  }
]}`

func TestRunTrunkList_FiltersAndSubportRendering(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/trunks", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"status": "ACTIVE", "admin_state_up": "true"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(trunkListBody))
	})

	o := &output.Options{Format: output.FormatTable}
	up := true
	f := &trunkListFlags{status: "ACTIVE", adminStateUp: &up, long: true}

	var buf bytes.Buffer
	if err := runTrunkList(context.Background(), networkClient(fakeServer), o, f, "", &buf); err != nil {
		t.Fatalf("runTrunkList error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"t1", "trunk-a", "parent-1", "Sub Ports",
		// Sub-ports render as port:type:segmentation-id.
		"sub-1:vlan:101", "sub-2:vlan:102",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("trunk list output missing %q\n---\n%s", want, out)
		}
	}
}

// TestRunTrunkDelete_AggregatesFailures asserts that a mid-list delete failure
// does not abort the remaining deletes and that the returned error names the
// failed ref.
func TestRunTrunkDelete_AggregatesFailures(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// resolveTrunkID's name lookup always misses here, so each ref falls back
	// to being treated as a literal ID (the documented zero-match behavior).
	fakeServer.Mux.HandleFunc("/trunks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"trunks": []}`))
	})

	var deleted []string
	fakeServer.Mux.HandleFunc("/trunks/t1", func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, "t1")
		w.WriteHeader(http.StatusNoContent)
	})
	fakeServer.Mux.HandleFunc("/trunks/bad", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	fakeServer.Mux.HandleFunc("/trunks/t2", func(w http.ResponseWriter, _ *http.Request) {
		deleted = append(deleted, "t2")
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	err := runTrunkDelete(context.Background(), networkClient(fakeServer), []string{"t1", "bad", "t2"}, &buf)
	if err == nil {
		t.Fatal("runTrunkDelete returned nil error; want a failure for the bad ref")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error missing failed ref %q: %v", "bad", err)
	}
	if len(deleted) != 2 || deleted[0] != "t1" || deleted[1] != "t2" {
		t.Errorf("deleted = %v, want both [t1 t2] attempted despite the failure between them", deleted)
	}
}

func TestRunTrunkCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	// The parent and sub port references are UUIDs already, but resolvePortID
	// still name-filters first; an empty result falls back to the literal ref.
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ports": []}`))
	})

	var gotMethod string
	fakeServer.Mux.HandleFunc("/trunks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"trunks": []}`))
			return
		}
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"trunk": {
          "name": "trunk-a",
          "port_id": "parent-1",
          "admin_state_up": true,
          "sub_ports": [{"port_id": "sub-1", "segmentation_type": "vlan", "segmentation_id": 101}]
        }}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"trunk": {
          "id": "t9", "name": "trunk-a", "port_id": "parent-1", "status": "DOWN",
          "admin_state_up": true, "sub_ports": []
        }}`))
	})

	o := &output.Options{Format: output.FormatTable}
	up := true
	f := &trunkCreateFlags{
		parentPort:   "parent-1",
		subports:     []string{"port=sub-1,segmentation-type=vlan,segmentation-id=101"},
		adminStateUp: &up,
	}

	var buf bytes.Buffer
	if err := runTrunkCreate(context.Background(), networkClient(fakeServer), o, "trunk-a", f, &buf); err != nil {
		t.Fatalf("runTrunkCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(buf.String(), "t9") {
		t.Errorf("output missing the new trunk ID\n---\n%s", buf.String())
	}
}

func TestParseSubports_Validation(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ports": []}`))
	})
	client := networkClient(fakeServer)

	tests := []struct {
		name    string
		spec    string
		wantErr string
	}{
		{name: "well formed", spec: "port=p1,segmentation-type=vlan,segmentation-id=7"},
		{name: "underscore keys", spec: "port=p1,segmentation_type=vlan,segmentation_id=7"},
		{name: "missing type", spec: "port=p1,segmentation-id=7", wantErr: "requires port, segmentation-type"},
		{name: "missing port", spec: "segmentation-type=vlan,segmentation-id=7", wantErr: "requires port, segmentation-type"},
		{name: "non-numeric id", spec: "port=p1,segmentation-type=vlan,segmentation-id=x", wantErr: "is not a number"},
		{name: "unknown key", spec: "port=p1,vlan=7", wantErr: "unknown key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSubports(context.Background(), client, []string{tc.spec})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseSubports() error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSubports() error = %v, want nil", err)
			}
			if len(got) != 1 || got[0].PortID != "p1" || got[0].SegmentationType != "vlan" || got[0].SegmentationID != 7 {
				t.Errorf("parseSubports() = %+v, want one vlan/7 sub-port on p1", got)
			}
		})
	}
}

func TestRunTrunkSubportList_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/trunks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"trunks": []}`))
	})
	var gotPath string
	fakeServer.Mux.HandleFunc("/trunks/t1/get_subports", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"sub_ports": [
          {"port_id": "sub-1", "segmentation_type": "vlan", "segmentation_id": 101}
        ]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runTrunkSubportList(context.Background(), networkClient(fakeServer), o, "t1", &buf); err != nil {
		t.Fatalf("runTrunkSubportList error: %v", err)
	}
	if gotPath != "/trunks/t1/get_subports" {
		t.Errorf("path = %q, want /trunks/t1/get_subports", gotPath)
	}
	for _, want := range []string{"Port", "Segmentation Type", "Segmentation ID", "sub-1", "vlan", "101"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("subport list output missing %q\n---\n%s", want, buf.String())
		}
	}
}

// Removing a sub-port keys on the port alone; the segmentation details are not
// part of the request body.
func TestRunTrunkSubportRemove_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/trunks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"trunks": []}`))
	})
	fakeServer.Mux.HandleFunc("/ports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ports": []}`))
	})
	var gotMethod string
	fakeServer.Mux.HandleFunc("/trunks/t1/remove_subports", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{"sub_ports": [{"port_id": "sub-1"}]}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id": "t1", "name": "trunk-a", "port_id": "parent-1", "sub_ports": []}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runTrunkSubportRemove(context.Background(), networkClient(fakeServer), o, "t1", []string{"sub-1"}, &buf)
	if err != nil {
		t.Fatalf("runTrunkSubportRemove error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
}

func TestRunExtensionList_RawFallbackRequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/extensions", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"extensions": [
          {"alias": "trunk", "name": "Trunk Extension", "description": "Provides trunks", "updated": "2016-01-01T10:00:00-00:00"},
          {"alias": "qos", "name": "Quality of Service", "description": "QoS policies", "updated": "2015-06-08T10:00:00-00:00"}
        ]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	client := networkClient(fakeServer)

	var buf bytes.Buffer
	if err := runExtensionList(context.Background(), client, o, false, &buf); err != nil {
		t.Fatalf("runExtensionList error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/extensions" {
		t.Errorf("path = %q, want /extensions", gotPath)
	}
	for _, want := range []string{"Name", "Alias", "Trunk Extension", "trunk", "qos"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("extension list output missing %q\n---\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "Provides trunks") {
		t.Errorf("default output should omit descriptions (--long adds them):\n%s", buf.String())
	}

	var long bytes.Buffer
	if err := runExtensionList(context.Background(), client, o, true, &long); err != nil {
		t.Fatalf("runExtensionList --long error: %v", err)
	}
	for _, want := range []string{"Description", "Provides trunks", "Updated"} {
		if !strings.Contains(long.String(), want) {
			t.Errorf("--long extension output missing %q\n---\n%s", want, long.String())
		}
	}
}

func TestRunExtensionShow_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/extensions/trunk", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"extension": {
          "alias": "trunk", "name": "Trunk Extension",
          "description": "Provides trunks", "updated": "2016-01-01T10:00:00-00:00"
        }}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runExtensionShow(context.Background(), networkClient(fakeServer), o, "trunk", &buf); err != nil {
		t.Fatalf("runExtensionShow error: %v", err)
	}
	if gotPath != "/extensions/trunk" {
		t.Errorf("path = %q, want /extensions/trunk", gotPath)
	}
	for _, want := range []string{"alias", "trunk", "Trunk Extension", "Provides trunks"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("extension show output missing %q\n---\n%s", want, buf.String())
		}
	}
}

// enableDisable's named-pair form backs --share/--no-share and --default/--no-default
// alongside the default --enable/--disable pair.
func TestEnableDisable_NamedPairs(t *testing.T) {
	newFlags := func() *pflag.FlagSet {
		fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
		fs.Bool("enable", false, "")
		fs.Bool("disable", false, "")
		fs.Bool("share", false, "")
		fs.Bool("no-share", false, "")
		return fs
	}
	tests := []struct {
		name  string
		args  []string
		names []string
		want  *bool
	}{
		{name: "neither given", args: nil, want: nil},
		{name: "enable", args: []string{"--enable"}, want: boolPtr(true)},
		{name: "disable", args: []string{"--disable"}, want: boolPtr(false)},
		{name: "share", args: []string{"--share"}, names: []string{"share", "no-share"}, want: boolPtr(true)},
		{name: "no-share", args: []string{"--no-share"}, names: []string{"share", "no-share"}, want: boolPtr(false)},
		// The default pair is unaffected by a --share on the same command line.
		{name: "share does not move enable", args: []string{"--share"}, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFlags()
			if err := fs.Parse(tc.args); err != nil {
				t.Fatalf("parse: %v", err)
			}
			onName, offName := "enable", "disable"
			if len(tc.names) == 2 {
				onName, offName = tc.names[0], tc.names[1]
			}
			on, _ := fs.GetBool(onName)
			off, _ := fs.GetBool(offName)

			var got *bool
			if len(tc.names) == 2 {
				got = enableDisable(fs, on, off, tc.names[0], tc.names[1])
			} else {
				got = enableDisable(fs, on, off)
			}
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("enableDisable() = %v, want nil", *got)
			case tc.want != nil && got == nil:
				t.Errorf("enableDisable() = nil, want %v", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Errorf("enableDisable() = %v, want %v", *got, *tc.want)
			}
		})
	}
}
