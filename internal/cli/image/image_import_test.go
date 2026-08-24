package image

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imageimport"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const testImageUUID = "5a3f2b1c-1111-4222-8333-444455556666"

func TestRunImageImport_WebDownloadRequestBody(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/images/"+testImageUUID+"/import", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestHeader(t, r, "X-Auth-Token", fakeclient.TokenID)
		th.TestJSONRequest(t, r, `{
          "method": {"name": "web-download", "uri": "https://example.invalid/cirros.qcow2"}
        }`)
		w.WriteHeader(http.StatusAccepted)
	})

	var buf bytes.Buffer
	err := runImageImport(context.Background(), imageClient(fakeServer), "cirros", testImageUUID,
		&imageImportFlags{resolved: imageimport.WebDownloadMethod,
			uri: "https://example.invalid/cirros.qcow2"}, &buf)
	if err != nil {
		t.Fatalf("runImageImport error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	out := buf.String()
	for _, want := range []string{"web-download", "cirros", "koc image show"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRunImageImport_GlanceDirectOmitsURI(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/images/"+testImageUUID+"/import", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestJSONRequest(t, r, `{"method": {"name": "glance-direct", "uri": ""}}`)
		w.WriteHeader(http.StatusAccepted)
	})

	var buf bytes.Buffer
	err := runImageImport(context.Background(), imageClient(fakeServer), testImageUUID, testImageUUID,
		&imageImportFlags{resolved: imageimport.GlanceDirectMethod}, &buf)
	if err != nil {
		t.Fatalf("runImageImport error: %v", err)
	}
}

// --all-stores and --store are the only import options that land outside the
// "method" object, so the assertion that matters is where they sit in the body.
func TestRunImageImport_StoreOptionsSitBesideMethod(t *testing.T) {
	tests := []struct {
		name      string
		stores    []string
		allStores bool
		wantBody  string
	}{
		{
			name:      "all stores",
			allStores: true,
			wantBody:  `{"all_stores": true, "method": {"name": "web-download", "uri": "https://example.invalid/i.qcow2"}}`,
		},
		{
			name:     "named stores",
			stores:   []string{"cheap", "fast"},
			wantBody: `{"stores": ["cheap", "fast"], "method": {"name": "web-download", "uri": "https://example.invalid/i.qcow2"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()

			fakeServer.Mux.HandleFunc("/images/"+testImageUUID+"/import", func(w http.ResponseWriter, r *http.Request) {
				th.TestMethod(t, r, http.MethodPost)
				th.TestJSONRequest(t, r, tc.wantBody)
				w.WriteHeader(http.StatusAccepted)
			})

			var buf bytes.Buffer
			err := runImageImport(context.Background(), imageClient(fakeServer), "img", testImageUUID,
				&imageImportFlags{resolved: imageimport.WebDownloadMethod,
					uri: "https://example.invalid/i.qcow2", stores: tc.stores, allStores: tc.allStores}, &buf)
			if err != nil {
				t.Fatalf("runImageImport error: %v", err)
			}
		})
	}
}

// resolveMethod carries the whole --method / --import-method / --uri contract, so
// it is table-tested directly against a parsed flag set.
func TestImageImportFlags_ResolveMethod(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantMethod imageimport.ImportMethod
		wantErr    string
	}{
		{
			name:       "uri alone defaults to web-download",
			args:       []string{"--uri=https://example.invalid/x.qcow2"},
			wantMethod: imageimport.WebDownloadMethod,
		},
		{
			name:       "explicit method",
			args:       []string{"--method=web-download", "--uri=https://example.invalid/x.qcow2"},
			wantMethod: imageimport.WebDownloadMethod,
		},
		{
			name:       "upstream --import-method spelling",
			args:       []string{"--import-method=web-download", "--uri=https://example.invalid/x.qcow2"},
			wantMethod: imageimport.WebDownloadMethod,
		},
		{
			name:       "both spellings agreeing",
			args:       []string{"--method=web-download", "--import-method=web-download", "--uri=https://example.invalid/x.qcow2"},
			wantMethod: imageimport.WebDownloadMethod,
		},
		{
			name:       "glance-direct needs no uri",
			args:       []string{"--method=glance-direct"},
			wantMethod: imageimport.GlanceDirectMethod,
		},
		{
			name:    "both spellings disagreeing",
			args:    []string{"--method=web-download", "--import-method=glance-direct", "--uri=https://x.invalid/y"},
			wantErr: "different values",
		},
		{
			name:    "no method and no uri",
			args:    []string{},
			wantErr: "no import method given",
		},
		{
			name:    "unknown method",
			args:    []string{"--method=telepathy"},
			wantErr: "unsupported import method",
		},
		{
			name:    "web-download without uri",
			args:    []string{"--method=web-download"},
			wantErr: "--uri is required",
		},
		{
			name:    "glance-direct with uri",
			args:    []string{"--method=glance-direct", "--uri=https://x.invalid/y"},
			wantErr: "web-download method only",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &imageImportFlags{}
			cmd := &cobra.Command{Use: "import"}
			fl := cmd.Flags()
			fl.StringVar(&f.method, "method", "", "")
			fl.StringVar(&f.importMethod, "import-method", "", "")
			fl.StringVar(&f.uri, "uri", "", "")
			if err := fl.Parse(tc.args); err != nil {
				t.Fatalf("parsing %v: %v", tc.args, err)
			}

			err := f.resolveMethod(cmd)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("resolveMethod() error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMethod() error = %v, want nil", err)
			}
			if f.resolved != tc.wantMethod {
				t.Errorf("resolveMethod() = %q, want %q", f.resolved, tc.wantMethod)
			}
		})
	}
}

func TestRunImageImportInfo_RequestAndOutput(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotPath string
	fakeServer.Mux.HandleFunc("/info/import", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		th.TestMethod(t, r, http.MethodGet)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
          "import-methods": {
            "description": "Import methods available.",
            "type": "array",
            "value": ["glance-direct", "web-download"]
          }
        }`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runImageImportInfo(context.Background(), imageClient(fakeServer), o, &buf); err != nil {
		t.Fatalf("runImageImportInfo error: %v", err)
	}
	if gotPath != "/info/import" {
		t.Errorf("path = %q, want /info/import", gotPath)
	}
	for _, want := range []string{"import-methods", "glance-direct", "web-download", "array"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("import info output missing %q\n---\n%s", want, buf.String())
		}
	}
}
