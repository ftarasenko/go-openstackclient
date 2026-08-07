package identity

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// catalogFixture serves GET /auth/catalog with two entries.
func catalogFixture(t *testing.T, fakeServer th.FakeServer, gotMethod, gotPath *string) {
	t.Helper()
	fakeServer.Mux.HandleFunc("/auth/catalog", func(w http.ResponseWriter, r *http.Request) {
		*gotMethod, *gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
          "catalog": [
            {"id": "c1", "name": "keystone", "type": "identity", "endpoints": [
               {"id": "e1", "interface": "public", "region": "RegionOne", "url": "https://keystone.example/v3"},
               {"id": "e2", "interface": "admin", "region": "RegionOne", "url": "https://keystone-admin.example/v3"}
             ]},
            {"id": "c2", "name": "nova", "type": "compute", "endpoints": [
               {"id": "e3", "interface": "public", "region": "RegionOne", "url": "https://nova.example/v2.1"}
             ]}
          ],
          "links": {"next": null}
        }`))
	})
}

func TestRunCatalogShow_ByName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	catalogFixture(t, fakeServer, &gotMethod, &gotPath)

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runCatalogShow(context.Background(), identityClient(fakeServer), o, "nova", &buf); err != nil {
		t.Fatalf("runCatalogShow error: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/auth/catalog" {
		t.Errorf("request = %s %s, want GET /auth/catalog", gotMethod, gotPath)
	}
	out := buf.String()
	for _, want := range []string{"c2", "nova", "compute", "public: https://nova.example/v2.1 (RegionOne)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "keystone") {
		t.Errorf("show must render only the matched entry\n---\n%s", out)
	}
}

// Upstream accepts the catalog *type* as well as the service name.
func TestRunCatalogShow_ByType(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	catalogFixture(t, fakeServer, &gotMethod, &gotPath)

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runCatalogShow(context.Background(), identityClient(fakeServer), o, "identity", &buf); err != nil {
		t.Fatalf("runCatalogShow error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "keystone") {
		t.Errorf("lookup by type failed\n---\n%s", out)
	}
	// Both endpoints render, one per line — the table layer keeps the newline.
	if !strings.Contains(out, "public: https://keystone.example/v3 (RegionOne)") ||
		!strings.Contains(out, "admin: https://keystone-admin.example/v3 (RegionOne)") {
		t.Errorf("both endpoints should render on separate lines\n---\n%s", out)
	}
}

func TestRunCatalogShow_NotFound(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotPath string
	catalogFixture(t, fakeServer, &gotMethod, &gotPath)

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runCatalogShow(context.Background(), identityClient(fakeServer), o, "swift", &buf)
	if err == nil {
		t.Fatal("expected an error for an absent service, got nil")
	}
	if !strings.Contains(err.Error(), "swift") {
		t.Errorf("error should name the service, got: %v", err)
	}
}
