package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// userDataFixture writes body to a file under t.TempDir() and returns its path,
// so every case below exercises the real read rather than a pre-loaded buffer.
func userDataFixture(t *testing.T, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("writing user-data fixture %s: %v", name, err)
	}
	return path
}

// userDataCases are the payloads that decide whether the file survives the trip.
// The last three are the regression: each is valid base64 once Go's decoder
// drops the newlines, so handing the raw bytes to gophercloud's
// servers.CreateOpts sends them verbatim, nova's lenient base64 check accepts
// them, and the guest is served the decoded garbage. Every "want" here is what
// Python's base64.b64encode produces, which is what upstream OSC sends.
var userDataCases = []struct {
	name string
	body []byte
	want string
}{
	{"cloud-config", []byte("#cloud-config\npackages: [fio]\n"), "I2Nsb3VkLWNvbmZpZwpwYWNrYWdlczogW2Zpb10K"},
	{"shell script", []byte("#!/bin/sh\necho hi\n"), "IyEvYmluL3NoCmVjaG8gaGkK"},
	{"alphanumeric, length divisible by four", []byte("runcmd\nls\n"), "cnVuY21kCmxzCg=="},
	{"single alphanumeric line", []byte("hostname\n"), "aG9zdG5hbWUK"},
	{"no newline at all", []byte("deadbeef"), "ZGVhZGJlZWY="},
	{"binary (gzip magic)", []byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}, "H4sIAAAAAAA="},
	{"already base64", []byte("IyEvYmluL3NoCg=="), "SXlFdlltbHVMM05vQ2c9PQ=="},
}

// TestReadUserData asserts the file is loaded from disk and encoded the way
// nova's schema wants it, including for the payloads that used to be passed
// through unencoded.
func TestReadUserData(t *testing.T) {
	for _, tc := range userDataCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readUserData(userDataFixture(t, "user-data", tc.body))
			if err != nil {
				t.Fatalf("readUserData: %v", err)
			}
			if got != tc.want {
				t.Errorf("readUserData = %q, want %q", got, tc.want)
			}
		})
	}

	// An empty file is not an error here: it encodes to "", and the caller
	// decides what that means (create warns and omits, rebuild refuses).
	if got, err := readUserData(userDataFixture(t, "empty", nil)); err != nil || got != "" {
		t.Errorf("readUserData(empty) = %q, %v; want \"\", nil", got, err)
	}
	if _, err := readUserData(filepath.Join(t.TempDir(), "absent")); err == nil ||
		!strings.Contains(err.Error(), "reading --user-data") {
		t.Errorf("readUserData(absent) err = %v, want a read failure naming the flag", err)
	}
}

// newUserDataCreateServer stands up a mock nova that records the created
// server's body, so a test can assert the exact user_data string on the wire.
func newUserDataCreateServer(t *testing.T, got *map[string]any) th.FakeServer {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors":[{"id":"2","name":"m1.small"}]}`))
	})
	fakeServer.Mux.HandleFunc("/servers", func(w http.ResponseWriter, r *http.Request) {
		*got, _ = decodeBody(t, r)["server"].(map[string]any)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","adminPass":"pw"}}`))
	})
	fakeServer.Mux.HandleFunc("/servers/new-id", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":{"id":"new-id","name":"boot","status":"ACTIVE"}}`))
	})
	return fakeServer
}

// TestRunServerCreate_UserDataEncoding is the regression test for the
// pass-through bug: every payload must reach nova base64-encoded, whatever its
// bytes happen to look like. It is driven from a real file so the whole path —
// open, read, encode, serialise — is under test.
func TestRunServerCreate_UserDataEncoding(t *testing.T) {
	for _, tc := range userDataCases {
		t.Run(tc.name, func(t *testing.T) {
			var gotServer map[string]any
			fakeServer := newUserDataCreateServer(t, &gotServer)

			f := &serverCreateFlags{
				image:    "img-uuid",
				flavor:   "m1.small",
				userData: userDataFixture(t, "user-data", tc.body),
			}
			var buf, warn bytes.Buffer
			o := &output.Options{Format: output.FormatTable}
			if err := runServerCreate(context.Background(), computeClient(fakeServer, "2.93"),
				o, "boot", f, &buf, &warn); err != nil {
				t.Fatalf("runServerCreate: %v", err)
			}
			if got := gotServer["user_data"]; got != tc.want {
				t.Errorf("user_data = %v, want %v", got, tc.want)
			}
			if warn.Len() != 0 {
				t.Errorf("unexpected warning: %q", warn.String())
			}
		})
	}
}

// TestRunServerCreate_UserDataEmptyFile pins the upstream behaviour for an empty
// file: the field is left out of the request rather than sent as "", the create
// still succeeds, and koc says so on stderr instead of doing it silently the way
// OSC does.
func TestRunServerCreate_UserDataEmptyFile(t *testing.T) {
	var gotServer map[string]any
	fakeServer := newUserDataCreateServer(t, &gotServer)

	empty := userDataFixture(t, "empty", nil)
	f := &serverCreateFlags{image: "img-uuid", flavor: "m1.small", userData: empty}
	var buf, warn bytes.Buffer
	o := &output.Options{Format: output.FormatTable}
	if err := runServerCreate(context.Background(), computeClient(fakeServer, "2.93"),
		o, "boot", f, &buf, &warn); err != nil {
		t.Fatalf("runServerCreate: %v", err)
	}
	if _, present := gotServer["user_data"]; present {
		t.Errorf("an empty --user-data file must be left out of the body, got %v", gotServer["user_data"])
	}
	if !strings.Contains(warn.String(), "is empty") || !strings.Contains(warn.String(), empty) {
		t.Errorf("warning = %q, want it to name the empty file", warn.String())
	}
}

// TestRunServerCreate_UserDataMissingFile asserts the typo is caught before any
// request goes out — the flavor lookup and the scheduler-hint resolution used to
// happen first, so a wrong path cost two round-trips.
func TestRunServerCreate_UserDataMissingFile(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var called bool
	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	})

	absent := filepath.Join(t.TempDir(), "absent")
	f := &serverCreateFlags{image: "img-uuid", flavor: "m1.small", userData: absent}
	var buf bytes.Buffer
	err := runServerCreate(context.Background(), computeClient(fakeServer, "2.93"),
		&output.Options{Format: output.FormatTable}, "boot", f, &buf, io.Discard)
	if err == nil || !strings.Contains(err.Error(), absent) {
		t.Fatalf("error = %v, want it to name %s", err, absent)
	}
	if called {
		t.Error("a missing --user-data file must fail before anything is sent")
	}
}
