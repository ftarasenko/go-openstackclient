package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunRegionCreate_PostsIDAndParent(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/regions", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"region": {"id": "RegionTwo", "parent_region_id": "RegionOne", "description": "second"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	err := runRegionCreate(context.Background(), identityClient(fakeServer), o, "RegionTwo", "RegionOne", "second", &out)
	if err != nil {
		t.Fatalf("runRegionCreate returned error: %v", err)
	}

	th.AssertEquals(t, "POST", gotMethod)
	region := body["region"].(map[string]any)
	th.AssertEquals(t, "RegionTwo", region["id"])
	th.AssertEquals(t, "RegionOne", region["parent_region_id"])
	if !strings.Contains(out.String(), "RegionTwo") {
		t.Errorf("output missing the created region:\n%s", out.String())
	}
}

func TestRunRegionCreate_OmittedIDLetsKeystoneGenerateIt(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/regions", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"region": {"id": "generated-uuid"}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	if err := runRegionCreate(context.Background(), identityClient(fakeServer), o, "", "", "", &out); err != nil {
		t.Fatalf("runRegionCreate returned error: %v", err)
	}
	// An omitted ID must not be sent as an empty string — keystone generates one.
	region := body["region"].(map[string]any)
	if _, present := region["id"]; present {
		t.Errorf("empty region ID was sent to the API: %#v", region)
	}
}

func TestRunRegionSet_OmittedDescriptionIsNotSent(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/regions/RegionOne", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"region": {"id": "RegionOne"}}`))
	})

	if err := runRegionSet(context.Background(), identityClient(fakeServer), "RegionOne", "RegionZero", "", false); err != nil {
		t.Fatalf("runRegionSet returned error: %v", err)
	}
	th.AssertEquals(t, "PATCH", gotMethod)
	region := body["region"].(map[string]any)
	th.AssertEquals(t, "RegionZero", region["parent_region_id"])
	if _, present := region["description"]; present {
		t.Errorf("description sent although the flag was not set: %#v", region)
	}
}

func TestRunRegionDelete_DeletesEachID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var deleted []string
	fakeServer.Mux.HandleFunc("/regions/", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "DELETE", r.Method)
		deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/regions/"))
		w.WriteHeader(http.StatusNoContent)
	})

	err := runRegionDelete(context.Background(), identityClient(fakeServer), []string{"RegionTwo", "RegionThree"})
	if err != nil {
		t.Fatalf("runRegionDelete returned error: %v", err)
	}
	th.AssertDeepEquals(t, []string{"RegionTwo", "RegionThree"}, deleted)
}

func TestRunServiceCreate_TypeIsPositionalAndEnableIsTriState(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var body map[string]any
	fakeServer.Mux.HandleFunc("/services", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "POST", r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"service": {"id": "svc-id", "type": "keyvrm", "name": "keyvrm", "enabled": true}}`))
	})

	var out bytes.Buffer
	o := &output.Options{Format: "value"}
	f := &serviceWriteFlags{name: "keyvrm"}
	if err := runServiceCreate(context.Background(), identityClient(fakeServer), o, "keyvrm", f, &out); err != nil {
		t.Fatalf("runServiceCreate returned error: %v", err)
	}

	svc := body["service"].(map[string]any)
	th.AssertEquals(t, "keyvrm", svc["type"])
	th.AssertEquals(t, "keyvrm", svc["name"])
	// Neither --enable nor --disable given: the field must be absent so keystone
	// applies its own default rather than being told "false".
	if _, present := svc["enabled"]; present {
		t.Errorf("enabled sent although neither --enable nor --disable was given: %#v", svc)
	}
}

func TestRunServiceSet_DisableSendsFalse(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	fakeServer.Mux.HandleFunc("/services", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"services": [{"id": "svc-id", "type": "keyvrm", "name": "keyvrm"}]}`))
	})
	var body map[string]any
	fakeServer.Mux.HandleFunc("/services/svc-id", func(w http.ResponseWriter, r *http.Request) {
		th.AssertEquals(t, "PATCH", r.Method)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service": {"id": "svc-id", "enabled": false}}`))
	})

	f := &serviceWriteFlags{disable: true}
	if err := runServiceSet(context.Background(), identityClient(fakeServer), "keyvrm", f, false, false); err != nil {
		t.Fatalf("runServiceSet returned error: %v", err)
	}
	svc := body["service"].(map[string]any)
	// --disable must survive as an explicit false rather than being dropped as a
	// zero value.
	th.AssertEquals(t, false, svc["enabled"])
}

func TestRunTokenRevoke_DeletesWithSubjectTokenHeader(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod, gotSubject string
	fakeServer.Mux.HandleFunc("/auth/tokens", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotSubject = r.Header.Get("X-Subject-Token")
		w.WriteHeader(http.StatusNoContent)
	})

	if err := runTokenRevoke(context.Background(), identityClient(fakeServer), "token-to-kill"); err != nil {
		t.Fatalf("runTokenRevoke returned error: %v", err)
	}
	th.AssertEquals(t, "DELETE", gotMethod)
	// The token being revoked travels in the header, not the URL — it is not the
	// token authenticating the call.
	th.AssertEquals(t, "token-to-kill", gotSubject)
}

func TestRunUserPasswordSet_PostsBothPasswords(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	var gotMethod string
	var body map[string]any
	fakeServer.Mux.HandleFunc("/users/user-id/password", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	err := runUserPasswordSet(context.Background(), identityClient(fakeServer), "user-id", "new-secret", "old-secret")
	if err != nil {
		t.Fatalf("runUserPasswordSet returned error: %v", err)
	}
	th.AssertEquals(t, "POST", gotMethod)
	user := body["user"].(map[string]any)
	th.AssertEquals(t, "new-secret", user["password"])
	th.AssertEquals(t, "old-secret", user["original_password"])
}

func TestRunUserPasswordSet_RefusesWithoutAUserID(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()

	err := runUserPasswordSet(context.Background(), identityClient(fakeServer), "", "new", "old")
	if err == nil {
		t.Fatal("expected an error when the authenticated user could not be determined")
	}
	// The message has to point at the command that changes someone else's
	// password, since that is the mistake being made.
	if !strings.Contains(err.Error(), "user set") {
		t.Errorf("error %q does not point at \"user set --password\"", err)
	}
}
