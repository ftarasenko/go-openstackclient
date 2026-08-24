package dns

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunZoneTransferRequestCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubZoneList(fakeServer)
	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/zones/z1/tasks/transfer_requests", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		th.TestJSONRequest(t, r, `{"target_project_id": "p2", "description": "handing over"}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
          "id": "tr1", "zone_id": "z1", "zone_name": "example.com.",
          "project_id": "p1", "target_project_id": "p2",
          "key": "SECRETKEY", "status": "ACTIVE", "description": "handing over"
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runZoneTransferRequestCreate(context.Background(), dnsShareClient(fakeServer), o,
		"example.com", "p2", "handing over", &buf)
	if err != nil {
		t.Fatalf("runZoneTransferRequestCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/zones/z1/tasks/transfer_requests" {
		t.Errorf("path = %q", gotPath)
	}
	// The key is the whole point of the create: it is what the target project needs.
	if !strings.Contains(buf.String(), "SECRETKEY") {
		t.Errorf("output must carry the transfer key\n---\n%s", buf.String())
	}
}

// Omitting --target-project leaves the offer open to any project holding the key,
// which is designate's own default — so no target_project_id is sent.
func TestRunZoneTransferRequestCreate_OpenOffer(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	stubZoneList(fakeServer)
	fakeServer.Mux.HandleFunc("/zones/z1/tasks/transfer_requests", func(w http.ResponseWriter, r *http.Request) {
		th.TestJSONRequest(t, r, `{}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": "tr1", "zone_id": "z1", "key": "K", "status": "ACTIVE"}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runZoneTransferRequestCreate(context.Background(), dnsShareClient(fakeServer), o, "z1", "", "", &buf); err != nil {
		t.Fatalf("runZoneTransferRequestCreate error: %v", err)
	}
}

func TestRunZoneTransferAcceptRequest_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/zones/tasks/transfer_accepts", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"key": "SECRETKEY", "zone_transfer_request_id": "tr1"}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
          "id": "ta1", "status": "COMPLETE", "project_id": "p2", "zone_id": "z1",
          "zone_transfer_request_id": "tr1"
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runZoneTransferAcceptRequest(context.Background(), dnsShareClient(fakeServer), o, "tr1", "SECRETKEY", &buf)
	if err != nil {
		t.Fatalf("runZoneTransferAcceptRequest error: %v", err)
	}
	if gotPath != "/zones/tasks/transfer_accepts" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(buf.String(), "COMPLETE") {
		t.Errorf("output missing the accept status\n---\n%s", buf.String())
	}
}

// The key is what authorises a transfer, so both flags are required.
func TestZoneTransferAcceptRequest_RequiresIDAndKey(t *testing.T) {
	for _, args := range [][]string{
		{"--transfer-id=tr1"},
		{"--key=K"},
		{},
	} {
		cmd := newZoneTransferAcceptRequestCommand(nil, &output.Options{Format: output.FormatTable})
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("%v: err = %v, want a required-flag error", args, err)
		}
	}
}

// "zone transfer accept" has no delete or set: designate's accept resource is
// Create/List/Get only, since accepting is a one-shot action.
func TestZoneTransferAccept_HasNoWriteVerbsBeyondCreate(t *testing.T) {
	cmd := newZoneTransferAcceptCommand(nil, &output.Options{})
	for _, sub := range cmd.Commands() {
		switch sub.Name() {
		case "request", "list", "show":
		default:
			t.Errorf("unexpected verb %q under \"zone transfer accept\"", sub.Name())
		}
	}
}

func TestRunDNSQuotaSet_OnlySendsGivenQuotas(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/quotas/p1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPatch)
		th.TestJSONRequest(t, r, `{"zone_records": 1000, "zones": 20}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "api_export_size": 1000, "recordset_records": 20,
          "zone_records": 1000, "zone_recordsets": 500, "zones": 20
        }`))
	})

	// recordsetRecords is populated but its flag was not given.
	f := &dnsQuotaSetFlags{zones: 20, zoneRecords: 1000, recordsetRecords: 999,
		changed: map[string]bool{"zones": true, "zone-records": true}, common: &commonOptions{}}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runDNSQuotaSet(context.Background(), dnsShareClient(fakeServer), o, "p1", f, &buf)
	if err != nil {
		t.Fatalf("runDNSQuotaSet error: %v", err)
	}
	for _, want := range []string{"zones", "20", "zone_records", "1000"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
}

func TestRunDNSQuotaSet_RejectsEmptyUpdate(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runDNSQuotaSet(context.Background(), dnsShareClient(fakeServer), o, "p1",
		&dnsQuotaSetFlags{changed: map[string]bool{}, common: &commonOptions{}}, &buf)
	if err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("expected a 'nothing to set' error, got %v", err)
	}
}

// dns quota reset has no typed gophercloud call (the package is Get/Update only),
// so the raw DELETE's method and URL are what the test pins down.
func TestRunDNSQuotaReset_RawDelete(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	fakeServer.Mux.HandleFunc("/quotas/p1", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	var buf bytes.Buffer
	if err := runDNSQuotaReset(context.Background(), dnsShareClient(fakeServer), "p1", &commonOptions{}, &buf); err != nil {
		t.Fatalf("runDNSQuotaReset error: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/quotas/p1" {
		t.Errorf("got %s %s, want DELETE /quotas/p1", gotMethod, gotPath)
	}
	if !strings.Contains(buf.String(), "Reset DNS quotas") {
		t.Errorf("unexpected output %q", buf.String())
	}
}

const tsigKeyBody = `{
  "id": "k1", "name": "transfer-key", "algorithm": "hmac-sha256",
  "secret": "SUPERSECRET", "scope": "POOL", "resource_id": "pool-1"
}`

// The secret is shared authentication material, so it must not appear in a listing
// — printing it there would spray it across terminals and CI logs.
func TestRunTSIGKeyList_OmitsTheSecret(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/tsigkeys", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"scope": "POOL"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tsigkeys": [` + tsigKeyBody + `]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runTSIGKeyList(context.Background(), dnsShareClient(fakeServer), o, &tsigKeyListFlags{scope: "POOL"}, &buf); err != nil {
		t.Fatalf("runTSIGKeyList error: %v", err)
	}
	for _, want := range []string{"k1", "transfer-key", "hmac-sha256", "POOL", "pool-1"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n---\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "SUPERSECRET") {
		t.Errorf("the TSIG secret must never appear in a listing:\n%s", buf.String())
	}
}

// "show" also withholds the secret unless it is asked for explicitly.
func TestRunTSIGKeyShow_SecretIsOptIn(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/tsigkeys", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tsigkeys": []}`))
	})
	fakeServer.Mux.HandleFunc("/tsigkeys/k1", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tsigKeyBody))
	})

	o := &output.Options{Format: output.FormatTable}
	client := dnsShareClient(fakeServer)

	var plain bytes.Buffer
	if err := runTSIGKeyShow(context.Background(), client, o, "k1", false, &plain); err != nil {
		t.Fatalf("runTSIGKeyShow error: %v", err)
	}
	if strings.Contains(plain.String(), "SUPERSECRET") {
		t.Errorf("secret leaked without --show-secret:\n%s", plain.String())
	}
	if !strings.Contains(plain.String(), "hmac-sha256") {
		t.Errorf("output missing the algorithm\n---\n%s", plain.String())
	}

	var withSecret bytes.Buffer
	if err := runTSIGKeyShow(context.Background(), client, o, "k1", true, &withSecret); err != nil {
		t.Fatalf("runTSIGKeyShow --show-secret error: %v", err)
	}
	if !strings.Contains(withSecret.String(), "SUPERSECRET") {
		t.Errorf("--show-secret should include the secret\n---\n%s", withSecret.String())
	}
}

func TestRunTSIGKeyCreate_RequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/tsigkeys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tsigkeys": []}`))
			return
		}
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{
          "name": "transfer-key", "algorithm": "hmac-sha256",
          "secret": "SUPERSECRET", "scope": "POOL", "resource_id": "pool-1"
        }`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(tsigKeyBody))
	})

	f := &tsigKeyWriteFlags{algorithm: "hmac-sha256", secret: "SUPERSECRET", scope: "POOL", resourceID: "pool-1"}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runTSIGKeyCreate(context.Background(), dnsShareClient(fakeServer), o, "transfer-key", f, &buf); err != nil {
		t.Fatalf("runTSIGKeyCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
}

// designate requires all four attributes; naming the missing one beats a 400.
func TestTSIGKeyCreate_RequiresEveryAttribute(t *testing.T) {
	base := map[string]string{
		"algorithm":   "hmac-sha256",
		"secret":      "s",
		"scope":       "POOL",
		"resource-id": "pool-1",
	}
	for omit := range base {
		args := []string{"transfer-key"}
		for flag, value := range base {
			if flag == omit {
				continue
			}
			args = append(args, "--"+flag+"="+value)
		}
		cmd := newTSIGKeyCreateCommand(nil, &output.Options{Format: output.FormatTable})
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "--"+omit) {
			t.Errorf("omitting --%s: err = %v, want it to name the missing flag", omit, err)
		}
	}
}

// dns quota list/set/reset must carry a cross-project header when the target is
// not the session's project — designate answers 403 to a bare cross-project GET
// or PATCH of /v2/quotas, which made the project argument a trap that only ever
// worked for your own project.
func TestDNSQuotaHeaders_CrossProject(t *testing.T) {
	tests := []struct {
		name        string
		common      *commonOptions
		wantAll     string
		wantSudo    string
		wantNoExtra bool
	}{
		{name: "auto all-projects", common: &commonOptions{allProjects: true}, wantAll: "true"},
		{name: "explicit sudo", common: &commonOptions{sudoProjectID: "p2"}, wantSudo: "p2"},
		{name: "own project sends nothing", common: &commonOptions{}, wantNoExtra: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			var gotAll, gotSudo string
			fakeServer.Mux.HandleFunc("/quotas/p2", func(w http.ResponseWriter, r *http.Request) {
				gotAll = r.Header.Get("X-Auth-All-Projects")
				gotSudo = r.Header.Get("X-Auth-Sudo-Project-ID")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"zones": 10, "zone_records": 500, "zone_recordsets": 500,
                  "recordset_records": 20, "api_export_size": 1000}`))
			})

			o := &output.Options{Format: output.FormatTable}
			var buf bytes.Buffer
			if err := runDNSQuotaList(context.Background(), dnsShareClient(fakeServer), o, "p2",
				tt.common, &buf); err != nil {
				t.Fatalf("runDNSQuotaList error: %v", err)
			}
			if gotAll != tt.wantAll {
				t.Errorf("X-Auth-All-Projects = %q, want %q", gotAll, tt.wantAll)
			}
			if gotSudo != tt.wantSudo {
				t.Errorf("X-Auth-Sudo-Project-ID = %q, want %q", gotSudo, tt.wantSudo)
			}
		})
	}
}

// The same headers must reach the write verbs, not just the read.
func TestRunDNSQuotaSetAndReset_CarryCrossProjectHeaders(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var patchAll, deleteAll string
	fakeServer.Mux.HandleFunc("/quotas/p2", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			deleteAll = r.Header.Get("X-Auth-All-Projects")
			w.WriteHeader(http.StatusNoContent)
		default:
			patchAll = r.Header.Get("X-Auth-All-Projects")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"zones": 20, "zone_records": 500, "zone_recordsets": 500,
              "recordset_records": 20, "api_export_size": 1000}`))
		}
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runDNSQuotaSet(context.Background(), dnsShareClient(fakeServer), o, "p2",
		&dnsQuotaSetFlags{zones: 20, changed: map[string]bool{"zones": true},
			common: &commonOptions{allProjects: true}}, &buf); err != nil {
		t.Fatalf("runDNSQuotaSet error: %v", err)
	}
	if patchAll != "true" {
		t.Errorf("set: X-Auth-All-Projects = %q, want true", patchAll)
	}
	buf.Reset()
	if err := runDNSQuotaReset(context.Background(), dnsShareClient(fakeServer), "p2",
		&commonOptions{allProjects: true}, &buf); err != nil {
		t.Fatalf("runDNSQuotaReset error: %v", err)
	}
	if deleteAll != "true" {
		t.Errorf("reset: X-Auth-All-Projects = %q, want true", deleteAll)
	}
}

// All three verbs must expose the upstream flags.
func TestDNSQuotaCommands_HaveCrossProjectFlags(t *testing.T) {
	a := &auth.Options{}
	o := &output.Options{Format: output.FormatTable}
	for name, cmd := range map[string]*cobra.Command{
		"list":  newDNSQuotaListCommand(a, o),
		"set":   newDNSQuotaSetCommand(a, o),
		"reset": newDNSQuotaResetCommand(a, o),
	} {
		for _, flag := range []string{"all-projects", "sudo-project-id", "project-id"} {
			if cmd.Flags().Lookup(flag) == nil {
				t.Errorf("dns quota %s is missing --%s", name, flag)
			}
		}
	}
}

// dnsQuotaTarget.resolve turns on all-projects only when the target differs from
// the session's project, and never overrides an explicit choice.
func TestDNSQuotaTarget_AutoAllProjects(t *testing.T) {
	a := &auth.Options{ProjectID: "11111111-1111-1111-1111-111111111111"}
	tests := []struct {
		name    string
		args    []string
		target  *dnsQuotaTarget
		common  *commonOptions
		wantAll bool
	}{
		{
			name:    "other project turns it on",
			args:    []string{"22222222-2222-2222-2222-222222222222"},
			target:  &dnsQuotaTarget{},
			common:  &commonOptions{},
			wantAll: true,
		},
		{
			name:    "own project leaves it off",
			args:    []string{"11111111-1111-1111-1111-111111111111"},
			target:  &dnsQuotaTarget{},
			common:  &commonOptions{},
			wantAll: false,
		},
		{
			name:    "no argument defaults to own project",
			args:    nil,
			target:  &dnsQuotaTarget{},
			common:  &commonOptions{},
			wantAll: false,
		},
		{
			name:    "--project-id is equivalent to the positional",
			args:    nil,
			target:  &dnsQuotaTarget{projectID: "22222222-2222-2222-2222-222222222222"},
			common:  &commonOptions{},
			wantAll: true,
		},
		{
			// An explicit sudo must not be silently joined by all-projects.
			name:    "explicit sudo-project-id wins",
			args:    []string{"22222222-2222-2222-2222-222222222222"},
			target:  &dnsQuotaTarget{},
			common:  &commonOptions{sudoProjectID: "22222222-2222-2222-2222-222222222222"},
			wantAll: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Both refs are UUIDs, so no identity round trip is needed and a nil
			// session is never dereferenced.
			got, err := tt.target.resolve(context.Background(), nil, a, tt.args, tt.common)
			if err != nil {
				t.Fatalf("resolve error: %v", err)
			}
			if got == "" {
				t.Fatal("resolve returned an empty project")
			}
			if tt.common.allProjects != tt.wantAll {
				t.Errorf("allProjects = %v, want %v", tt.common.allProjects, tt.wantAll)
			}
		})
	}
}

// A positional and a conflicting --project-id is an operator error, not a
// silent pick.
func TestDNSQuotaTarget_ConflictingProjects(t *testing.T) {
	target := &dnsQuotaTarget{projectID: "22222222-2222-2222-2222-222222222222"}
	_, err := target.resolve(context.Background(), nil, &auth.Options{ProjectID: "p0"},
		[]string{"33333333-3333-3333-3333-333333333333"}, &commonOptions{})
	if err == nil || !strings.Contains(err.Error(), "conflicting projects") {
		t.Fatalf("expected a conflicting-projects error, got %v", err)
	}
}
