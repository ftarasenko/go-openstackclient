package compute

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// With no owner filter, the list carries no user_id and no User ID column.
func TestRunKeypairList_NoOwnerFilter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-keypairs", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		if r.URL.Query().Has("user_id") {
			t.Errorf("unfiltered list should not send user_id, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keypairs": [
          {"keypair": {"name": "mine", "fingerprint": "aa:bb", "type": "ssh"}}
        ]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runKeypairList(context.Background(), computeClient(fakeServer, "latest"), o, nil, &buf); err != nil {
		t.Fatalf("runKeypairList error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "mine") {
		t.Errorf("output missing the keypair\n---\n%s", out)
	}
	if strings.Contains(out, "User ID") {
		t.Errorf("single-owner listing should not add a User ID column:\n%s", out)
	}
}

// --user sends nova's native user_id filter (microversion 2.10+) for one user, and
// still omits the User ID column since every row has the same owner.
func TestRunKeypairList_SingleUserFilter(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/os-keypairs", func(w http.ResponseWriter, r *http.Request) {
		th.TestFormValues(t, r, map[string]string{"user_id": "u1"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keypairs": [
          {"keypair": {"name": "theirs", "fingerprint": "cc:dd", "type": "ssh"}}
        ]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	if err := runKeypairList(context.Background(), computeClient(fakeServer, "latest"), o, []string{"u1"}, &buf); err != nil {
		t.Fatalf("runKeypairList error: %v", err)
	}
	if strings.Contains(buf.String(), "User ID") {
		t.Errorf("one-user listing should not add a User ID column:\n%s", buf.String())
	}
}

// --project expands to several users, so the listing fans out one request per
// user and gains a User ID column to keep the rows distinguishable.
func TestRunKeypairList_MultipleOwnersFanOutAndLabel(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var seen []string
	fakeServer.Mux.HandleFunc("/os-keypairs", func(w http.ResponseWriter, r *http.Request) {
		userID := r.URL.Query().Get("user_id")
		seen = append(seen, userID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keypairs": [
          {"keypair": {"name": "key-` + userID + `", "fingerprint": "aa:bb", "type": "ssh"}}
        ]}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runKeypairList(context.Background(), computeClient(fakeServer, "latest"), o, []string{"u1", "u2"}, &buf)
	if err != nil {
		t.Fatalf("runKeypairList error: %v", err)
	}
	if len(seen) != 2 || seen[0] != "u1" || seen[1] != "u2" {
		t.Errorf("expected one request per user (u1, u2), got %v", seen)
	}
	out := buf.String()
	for _, want := range []string{"User ID", "key-u1", "key-u2", "u1", "u2"} {
		if !strings.Contains(out, want) {
			t.Errorf("multi-owner output missing %q\n---\n%s", want, out)
		}
	}
}

func TestKeypairList_RejectsUserAndProjectTogether(t *testing.T) {
	cmd := newKeypairListCommand(nil, &output.Options{Format: output.FormatTable})
	cmd.SetArgs([]string{"--user=alice", "--project=demo"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected a mutual-exclusion error, got %v", err)
	}
}

// --public-key bypasses the table layer entirely so the key can be redirected
// into an authorized_keys file.
func TestRunKeypairShow_PublicKeyOnly(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	const pubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample alice@host"
	fakeServer.Mux.HandleFunc("/os-keypairs/mykey", func(w http.ResponseWriter, r *http.Request) {
		th.TestMethod(t, r, http.MethodGet)
		th.TestFormValues(t, r, map[string]string{"user_id": "u1"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keypair": {
          "name": "mykey", "fingerprint": "aa:bb", "type": "ssh",
          "user_id": "u1", "public_key": "` + pubKey + `"
        }}`))
	})

	o := &output.Options{Format: output.FormatTable}
	var buf bytes.Buffer
	err := runKeypairShow(context.Background(), computeClient(fakeServer, "latest"), o, "mykey", "u1", true, &buf)
	if err != nil {
		t.Fatalf("runKeypairShow error: %v", err)
	}
	if got := strings.TrimRight(buf.String(), "\n"); got != pubKey {
		t.Errorf("--public-key output = %q, want exactly the key %q", got, pubKey)
	}

	// Without the flag, the normal Field/Value view is rendered.
	var table bytes.Buffer
	err = runKeypairShow(context.Background(), computeClient(fakeServer, "latest"), o, "mykey", "u1", false, &table)
	if err != nil {
		t.Fatalf("runKeypairShow error: %v", err)
	}
	for _, want := range []string{"Name", "Fingerprint", "Public Key"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table output missing %q\n---\n%s", want, table.String())
		}
	}
}
