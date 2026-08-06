package image

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunImageCreate_VisibilityIDAndTags(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	fakeServer.Mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		th.TestJSONRequest(t, r, `{
          "name": "cirros",
          "id": "`+testImageUUID+`",
          "disk_format": "qcow2",
          "container_format": "bare",
          "tags": ["stable", "linux"],
          "visibility": "shared"
        }`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
          "id": "` + testImageUUID + `", "name": "cirros", "status": "queued",
          "visibility": "shared", "tags": ["stable", "linux"]
        }`))
	})

	f := &imageCreateFlags{
		diskFormat:      "qcow2",
		containerFormat: "bare",
		visibility:      "shared",
		id:              testImageUUID,
		tag:             []string{"stable", "linux"},
	}
	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runImageCreate(context.Background(), imageClient(fakeServer), o, "cirros", f, &buf); err != nil {
		t.Fatalf("runImageCreate error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if !strings.Contains(buf.String(), "shared") {
		t.Errorf("output missing the visibility\n---\n%s", buf.String())
	}
}

func TestResolveImageVisibility(t *testing.T) {
	ptr := func(v images.ImageVisibility) *images.ImageVisibility { return &v }
	tests := []struct {
		name       string
		visibility string
		public     bool
		private    bool
		want       *images.ImageVisibility
		wantErr    string
	}{
		// Saying nothing must stay nothing, so glance's own default applies rather
		// than koc asserting one.
		{name: "unset", want: nil},
		{name: "--public shorthand", public: true, want: ptr(images.ImageVisibilityPublic)},
		{name: "--private shorthand", private: true, want: ptr(images.ImageVisibilityPrivate)},
		{name: "explicit public", visibility: "public", want: ptr(images.ImageVisibilityPublic)},
		{name: "explicit private", visibility: "private", want: ptr(images.ImageVisibilityPrivate)},
		// shared and community are reachable only through --visibility.
		{name: "shared", visibility: "shared", want: ptr(images.ImageVisibilityShared)},
		{name: "community", visibility: "community", want: ptr(images.ImageVisibilityCommunity)},
		{name: "unknown", visibility: "secret", wantErr: "unsupported --visibility"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveImageVisibility(tc.visibility, tc.public, tc.private)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("visibility = %q, want nil", *got)
			case tc.want != nil && got == nil:
				t.Errorf("visibility = nil, want %q", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Errorf("visibility = %q, want %q", *got, *tc.want)
			}
		})
	}
}

// --visibility and the shorthands overlap, so asking for both is rejected at
// parse time rather than one silently winning.
func TestImageCreate_VisibilityAndShorthandsAreExclusive(t *testing.T) {
	for _, args := range [][]string{
		{"cirros", "--public", "--visibility=shared"},
		{"cirros", "--private", "--visibility=public"},
		{"cirros", "--public", "--private"},
	} {
		cmd := newImageCreateCommand(nil, &output.Options{Format: output.FormatTable})
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Errorf("%v should have been rejected as contradictory", args)
		}
	}
}
