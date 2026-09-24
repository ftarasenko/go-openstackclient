package image

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// execImage runs argv through the real command tree (Execute → RunE → seam)
// with every service pointed at fakeServer.
func execImage(t *testing.T, fakeServer th.FakeServer, argv ...string) (string, error) {
	t.Helper()
	a := &auth.Options{VolumeAPIVersion: "3.1"}
	provider := &gophercloud.ProviderClient{
		TokenID:      "fake-token",
		IdentityBase: fakeServer.Server.URL + "/",
		EndpointLocator: func(gophercloud.EndpointOpts) (string, error) {
			return fakeServer.Server.URL + "/", nil
		},
	}
	a.SetAuthenticatorForTest(func(context.Context) (*auth.Client, error) {
		return a.NewClientForTest(provider, gophercloud.EndpointOpts{}), nil
	})
	root := &cobra.Command{Use: "koc"}
	root.AddCommand(NewCommand(a, &output.Options{Format: "table"}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(argv)
	root.SilenceUsage = true
	root.SilenceErrors = true
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

func TestExec_ImageCreate_FileAndProjectName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/v3/projects", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"name": "demo"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"projects": [{"id": "proj-1", "name": "demo"}]}`))
	})
	var owner, uploaded, size string
	fakeServer.Mux.HandleFunc("/v2/images", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"owner":"proj-1"`) {
			owner = "proj-1"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(createdImage))
	})
	fakeServer.Mux.HandleFunc("/v2/images/"+testImageUUID+"/file", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		uploaded, size = string(raw), r.Header.Get("X-OpenStack-Image-Size")
		w.WriteHeader(http.StatusNoContent)
	})
	fakeServer.Mux.HandleFunc("/v2/images/"+testImageUUID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": "` + testImageUUID + `", "name": "img", "status": "active"}`))
	})

	path := filepath.Join(t.TempDir(), "img.raw")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := execImage(t, fakeServer, "image", "create", "img", "--file", path, "--project", "demo")
	if err != nil {
		t.Fatalf("image create: %v\n%s", err, out)
	}
	if owner != "proj-1" || uploaded != "abc" || size != "3" {
		t.Errorf("owner=%q uploaded=%q size=%q, want proj-1/abc/3", owner, uploaded, size)
	}
	if !strings.Contains(out, "active") {
		t.Errorf("output:\n%s", out)
	}
}

func TestExec_ImageCreate_VolumeByName(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/volumes/detail", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"name": "data"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"volumes": [{"id": "vol-1", "name": "data"}]}`))
	})
	fakeServer.Mux.HandleFunc("/volumes/vol-1/action", func(w http.ResponseWriter, r *http.Request) {
		th.TestHeader(t, r, "OpenStack-API-Version", "volume 3.1")
		th.TestJSONRequest(t, r, `{"os-volume_upload_image": {"image_name": "img",
			"disk_format": "raw", "container_format": "bare", "visibility": "public", "protected": true}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"os-volume_upload_image": {"id": "vol-1", "image_id": "img-9", "status": "uploading"}}`))
	})

	out, err := execImage(t, fakeServer, "image", "create", "img", "--volume", "data", "--public", "--protected", "--tag", "x")
	if err != nil {
		t.Fatalf("image create --volume: %v\n%s", err, out)
	}
	if !strings.Contains(out, "img-9") || !strings.Contains(out, "--tag is ignored") {
		t.Errorf("output:\n%s", out)
	}
}
