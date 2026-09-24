package image

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

const createdImage = `{"id": "` + testImageUUID + `", "name": "img", "status": "queued"}`

// imageCreateServer mocks POST /images, the data PUTs, GET and DELETE of the
// created image, recording what each one received.
type imageCreateServer struct {
	body          map[string]any
	importMethods string
	putStatus     int
	puts          map[string]*http.Request
	putData       map[string]string
	importBody    string
	deleted       bool
}

func newImageCreateServer(t *testing.T, fakeServer th.FakeServer) *imageCreateServer {
	t.Helper()
	s := &imageCreateServer{putStatus: http.StatusNoContent, puts: map[string]*http.Request{}, putData: map[string]string{}}
	fakeServer.Mux.HandleFunc("/images", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &s.body); err != nil {
			t.Fatalf("decoding create body: %v", err)
		}
		if s.importMethods != "" {
			w.Header().Set("OpenStack-image-import-methods", s.importMethods)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(createdImage))
	})
	fakeServer.Mux.HandleFunc("/images/"+testImageUUID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			s.deleted = true
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id": "` + testImageUUID + `", "name": "img", "status": "active", "size": 5}`))
		default:
			t.Errorf("unexpected %s on the image", r.Method)
		}
	})
	for _, part := range []string{"file", "stage"} {
		fakeServer.Mux.HandleFunc("/images/"+testImageUUID+"/"+part, func(w http.ResponseWriter, r *http.Request) {
			th.TestMethod(t, r, http.MethodPut)
			raw, _ := io.ReadAll(r.Body)
			s.puts[part], s.putData[part] = r, string(raw)
			w.WriteHeader(s.putStatus)
		})
	}
	fakeServer.Mux.HandleFunc("/images/"+testImageUUID+"/import", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		raw, _ := io.ReadAll(r.Body)
		s.importBody = string(raw)
		w.WriteHeader(http.StatusAccepted)
	})
	return s
}

func TestRunImageCreate_DefaultsOwnerProtectedCommunity(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	s := newImageCreateServer(t, fakeServer)

	f := &imageCreateFlags{owner: "proj-1", protected: true, community: true}
	var buf bytes.Buffer
	if err := runImageCreate(context.Background(), imageClient(fakeServer), &output.Options{Format: output.FormatTable}, "img", f, nil, &buf); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name": "img", "disk_format": "raw", "container_format": "bare",
		"owner": "proj-1", "protected": true, "visibility": "community",
	}
	for k, v := range want {
		if s.body[k] != v {
			t.Errorf("body[%q] = %v, want %v", k, s.body[k], v)
		}
	}
	if len(s.puts) != 0 {
		t.Errorf("no data was given but %d PUT(s) were sent", len(s.puts))
	}
}

func TestRunImageCreate_UploadSendsSizeHeader(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	s := newImageCreateServer(t, fakeServer)

	var progress bytes.Buffer
	src := &imageSource{r: strings.NewReader("hello"), size: 5, progress: &progress}
	var buf bytes.Buffer
	if err := runImageCreate(context.Background(), imageClient(fakeServer), &output.Options{Format: output.FormatTable},
		"img", &imageCreateFlags{}, src, &buf); err != nil {
		t.Fatal(err)
	}
	r := s.puts["file"]
	if r == nil {
		t.Fatal("no PUT /file")
	}
	if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := r.Header.Get("X-OpenStack-Image-Size"); got != "5" {
		t.Errorf("X-OpenStack-Image-Size = %q, want 5", got)
	}
	if s.putData["file"] != "hello" {
		t.Errorf("uploaded %q, want hello", s.putData["file"])
	}
	if !strings.Contains(buf.String(), "active") {
		t.Errorf("output should show the re-fetched record:\n%s", buf.String())
	}
	if !strings.HasSuffix(progress.String(), "100%\n") {
		t.Errorf("progress = %q, want it to end at 100%%", progress.String())
	}
}

// A failed upload must not leave a queued image behind.
func TestRunImageCreate_FailedUploadDeletesImage(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	s := newImageCreateServer(t, fakeServer)
	s.putStatus = http.StatusUnsupportedMediaType

	err := runImageCreate(context.Background(), imageClient(fakeServer), &output.Options{Format: output.FormatTable},
		"img", &imageCreateFlags{}, &imageSource{r: strings.NewReader("x")}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "partial image was deleted") {
		t.Fatalf("error = %v, want the upload failure plus the cleanup note", err)
	}
	if !s.deleted {
		t.Error("image was not deleted after the failed upload")
	}
}

func TestRunImageCreate_ImportStagesThenImports(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	s := newImageCreateServer(t, fakeServer)
	s.importMethods = "glance-direct,web-download"

	if err := runImageCreate(context.Background(), imageClient(fakeServer), &output.Options{Format: output.FormatTable},
		"img", &imageCreateFlags{useImport: true}, &imageSource{r: strings.NewReader("data")}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if s.putData["stage"] != "data" || s.puts["file"] != nil {
		t.Errorf("want data staged, not uploaded: stage=%q file=%v", s.putData["stage"], s.puts["file"] != nil)
	}
	th.CheckJSONEquals(t, `{"method": {"name": "glance-direct", "uri": ""}}`, json.RawMessage(s.importBody))
}

func TestRunImageCreate_ImportUnsupportedDeletesImage(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	s := newImageCreateServer(t, fakeServer)
	s.importMethods = "web-download"

	err := runImageCreate(context.Background(), imageClient(fakeServer), &output.Options{Format: output.FormatTable},
		"img", &imageCreateFlags{useImport: true}, &imageSource{r: strings.NewReader("data")}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "glance-direct") {
		t.Fatalf("error = %v, want a glance-direct error", err)
	}
	if len(s.puts) != 0 || !s.deleted {
		t.Errorf("puts=%d deleted=%v, want no data sent and the image deleted", len(s.puts), s.deleted)
	}
}

// Every one of these fails before auth (a nil *auth.Options would panic) and
// before any image is created.
func TestImageCreate_RejectedBeforeCreate(t *testing.T) {
	tests := map[string][]string{
		"bad disk format":      {"img", "--disk-format", "qcow3"},
		"bad container format": {"img", "--container-format", "tar"},
		"v1 option":            {"img", "--location", "http://example.com/x"},
		"non-positive size":    {"img", "--size", "0"},
		"missing file":         {"img", "--file", filepath.Join(t.TempDir(), "absent.iso")},
		"file and volume":      {"img", "--file", "x", "--volume", "v"},
		"two visibilities":     {"img", "--community", "--shared"},
		"size without data":    {"img", "--size", "10", "--volume", "v"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			cmd := newImageCreateCommand(nil, &output.Options{Format: output.FormatTable})
			cmd.SetArgs(args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err == nil {
				t.Errorf("%v was accepted", args)
			}
		})
	}
}

func TestImageCreate_FormatDefaults(t *testing.T) {
	cmd := newImageCreateCommand(nil, &output.Options{})
	for flag, want := range map[string]string{"disk-format": "raw", "container-format": "bare"} {
		if got := cmd.Flags().Lookup(flag).DefValue; got != want {
			t.Errorf("--%s default = %q, want %q", flag, got, want)
		}
	}
}

func TestStdinSource(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devNull.Close() }()
	if src := stdinSource(devNull); src != nil {
		t.Error("a character device must not be read as image data")
	}

	path := filepath.Join(t.TempDir(), "img.raw")
	if err := os.WriteFile(path, []byte("12345678"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if src := stdinSource(file); src == nil || src.size != 8 {
		t.Errorf("redirected file: got %+v, want a source of size 8", src)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if src := stdinSource(r); src == nil || src.size != 0 {
		t.Errorf("pipe: got %+v, want a source of unknown size", src)
	}
}

func TestProgressReader_SeekRestarts(t *testing.T) {
	var out bytes.Buffer
	p := &progressReader{r: strings.NewReader("abcd"), total: 4, w: &out, last: -1}
	if _, err := io.ReadAll(p); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if p.done != 0 {
		t.Errorf("done = %d after rewind, want 0", p.done)
	}
	if strings.Count(out.String(), "100%") != 1 {
		t.Errorf("progress = %q", out.String())
	}
}

func volumeClient(fakeServer th.FakeServer, mv string) *gophercloud.ServiceClient {
	sc := fakeclient.ServiceClient(fakeServer)
	sc.Type = "block-storage"
	sc.Microversion = mv
	return sc
}

func TestRunImageCreateFromVolume(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/volumes/vol-1/action", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodPost)
		th.TestHeader(t, r, "OpenStack-API-Version", "volume 3.1")
		th.TestJSONRequest(t, r, `{"os-volume_upload_image": {
			"image_name": "img", "force": true, "disk_format": "qcow2",
			"container_format": "bare", "visibility": "private"}}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"os-volume_upload_image": {"id": "vol-1", "image_id": "img-9",
			"image_name": "img", "status": "uploading", "volume_type": {"name": "ceph"}}}`))
	})

	var buf bytes.Buffer
	f := &imageCreateFlags{diskFormat: "qcow2", force: true}
	if err := runImageCreateFromVolume(context.Background(), volumeClient(fakeServer, "3.1"),
		&output.Options{Format: output.FormatTable}, "img", "vol-1", f, &buf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"img-9", "uploading", "ceph"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q:\n%s", want, buf.String())
		}
	}
}

func TestRunImageCreateFromVolume_VisibilityNeeds31(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	err := runImageCreateFromVolume(context.Background(), volumeClient(fakeServer, "3.0"),
		&output.Options{Format: output.FormatTable}, "img", "vol-1", &imageCreateFlags{public: true}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "3.1") {
		t.Fatalf("error = %v, want the 3.1 requirement", err)
	}
}

func TestVolumeAtLeast31(t *testing.T) {
	for mv, want := range map[string]bool{"latest": true, "3.1": true, "3.70": true, "3.0": false, "": false, "2.9": false} {
		if got := volumeAtLeast31(mv); got != want {
			t.Errorf("volumeAtLeast31(%q) = %v, want %v", mv, got, want)
		}
	}
}

func TestOpenSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "img.raw")
	if err := os.WriteFile(path, []byte("123"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newImageCreateCommand(nil, &output.Options{})
	cmd.SetIn(strings.NewReader("piped"))
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	f := &imageCreateFlags{file: path, progress: true}
	src, closeSrc, err := f.openSource(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSrc()
	if src.size != 3 || src.progress == nil {
		t.Errorf("--file: got size %d progress %v, want 3 and a progress writer", src.size, src.progress != nil)
	}

	f = &imageCreateFlags{size: 42}
	src, _, err = f.openSource(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := io.ReadAll(src.r); string(data) != "piped" || src.size != 42 {
		t.Errorf("stdin: got %q size %d, want the piped data with --size 42", data, src.size)
	}

	f = &imageCreateFlags{volume: "v"}
	if src, _, err = f.openSource(cmd); err != nil || src != nil {
		t.Errorf("--volume must not read stdin: src=%v err=%v", src, err)
	}
}
