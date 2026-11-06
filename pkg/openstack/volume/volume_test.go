package volume_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/attachments"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"

	"github.com/ftarasenko/koc/pkg/openstack/volume"
)

const (
	volID = "0b6c5e9a-1f2d-4c3b-9a8e-7d6f5e4c3b2a"
	atID  = "9e8d7c6b-5a4f-4e3d-8c2b-1a0f9e8d7c6b"
	srvID = "4a3b2c1d-0e9f-4a8b-8c7d-6e5f4a3b2c1d"
)

type call struct{ Method, Path, Query, Version, Body string }

// cinder is a fake block-storage endpoint that records every request and
// answers it from routes, keyed "METHOD /path".
type cinder struct {
	mu   sync.Mutex
	seen []call
}

type route func(r *http.Request) (int, string)

func newCinder(t *testing.T, microversion string, routes map[string]route) (*cinder, *gophercloud.ServiceClient) {
	t.Helper()
	fs := th.SetupHTTP()
	t.Cleanup(fs.Teardown)
	c := &cinder{}
	fs.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		var v any
		if json.Unmarshal(raw, &v) == nil {
			b, _ := json.Marshal(v)
			body = string(b)
		}
		c.mu.Lock()
		c.seen = append(c.seen, call{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("OpenStack-API-Version"), body})
		c.mu.Unlock()
		reply, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			reply = func(*http.Request) (int, string) {
				return http.StatusNotFound, `{"itemNotFound":{"code":404,"message":"not found"}}`
			}
		}
		status, out := reply(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, out)
	})
	sc := fakeclient.ServiceClient(fs)
	sc.Type = "block-storage"
	sc.Microversion = microversion
	return c, sc
}

func (c *cinder) take() []call {
	c.mu.Lock()
	defer c.mu.Unlock()
	got := c.seen
	c.seen = nil
	return got
}

func (c *cinder) only(t *testing.T) call {
	t.Helper()
	got := c.take()
	if len(got) != 1 {
		t.Fatalf("requests = %+v; want exactly one", got)
	}
	return got[0]
}

func fixed(status int, body string) route {
	return func(*http.Request) (int, string) { return status, body }
}

// sequence answers each request with the next body, repeating the last.
func sequence(bodies ...string) route {
	var mu sync.Mutex
	return func(*http.Request) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		b := bodies[0]
		if len(bodies) > 1 {
			bodies = bodies[1:]
		}
		if b == "" {
			return http.StatusInternalServerError, `{"computeFault":{"code":500,"message":"boom"}}`
		}
		return http.StatusOK, b
	}
}

func volBody(fields string) string {
	return `{"volume":{"id":"` + volID + `"` + fields + `}}`
}

func TestListVolumes_FiltersPagesAndCaps(t *testing.T) {
	c, sc := newCinder(t, "3.70", map[string]route{
		"GET /volumes/detail": func(r *http.Request) (int, string) {
			if r.URL.Query().Get("marker") == "" {
				return 200, `{"volumes":[{"id":"a","name":"one"},{"id":"b","name":"two"}],
					"volumes_links":[{"rel":"next","href":"http://` + r.Host + `/volumes/detail?marker=b"}]}`
			}
			return 200, `{"volumes":[{"id":"c","name":"three"}]}`
		},
	})
	ctx := context.Background()
	opts := volume.ListOpts{ListOptsBuilder: volumes.ListOpts{AllTenants: true, Name: "one"}, UserID: "u1"}
	all, err := volume.ListVolumes(ctx, sc, opts, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListVolumes = %d, %v; want 3 across two pages", len(all), err)
	}
	got := c.take()
	if q := got[0].Query; q != "all_tenants=true&name=one&user_id=u1" {
		t.Errorf("query = %q", q)
	}
	if got[0].Version != "volume 3.70" {
		t.Errorf("microversion header = %q", got[0].Version)
	}

	capped, err := volume.ListVolumes(ctx, sc, volumes.ListOpts{}, 1)
	if err != nil || len(capped) != 1 || capped[0].ID != "a" {
		t.Fatalf("capped = %+v, %v", capped, err)
	}
	if n := len(c.take()); n != 1 {
		t.Errorf("capped listing made %d requests; want 1", n)
	}
}

func TestListOpts_NoUserLeavesTheQuery(t *testing.T) {
	q, err := volume.ListOpts{ListOptsBuilder: volumes.ListOpts{Status: "available"}}.ToVolumeListQuery()
	if err != nil || q != "?status=available" {
		t.Fatalf("query = %q, %v", q, err)
	}
}

type badQuery string

func (b badQuery) ToVolumeListQuery() (string, error) {
	if b == "" {
		return "", errors.New("bad opts")
	}
	return string(b), nil
}

func TestListOpts_PassesErrorsOn(t *testing.T) {
	for _, base := range []badQuery{"", "%zz"} {
		if _, err := (volume.ListOpts{ListOptsBuilder: base, UserID: "u1"}).ToVolumeListQuery(); err == nil {
			t.Errorf("base %q: no error", base)
		}
	}
}

func TestMetadataUpdateOpts_SendsThePairs(t *testing.T) {
	m, err := volume.MetadataUpdateOpts{Metadata: map[string]string{"k": "v"}}.ToVolumeUpdateMap()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	if string(b) != `{"volume":{"metadata":{"k":"v"}}}` {
		t.Errorf("body = %s", b)
	}
}

func TestVolumeCRUD(t *testing.T) {
	c, sc := newCinder(t, "3.70", map[string]route{
		"POST /volumes":            fixed(202, volBody(`,"status":"creating"`)),
		"GET /volumes/" + volID:    fixed(200, volBody(`,"status":"available","metadata":{"a":"1"}`)),
		"PUT /volumes/" + volID:    fixed(200, volBody(`,"name":"renamed"`)),
		"DELETE /volumes/" + volID: fixed(202, ``),
	})
	ctx := context.Background()

	v, err := volume.CreateVolume(ctx, sc, volumes.CreateOpts{Name: "data", Size: 10}, nil)
	if err != nil || v.Status != "creating" {
		t.Fatalf("CreateVolume = %+v, %v", v, err)
	}
	if got := c.only(t); got.Body != `{"volume":{"name":"data","size":10}}` {
		t.Errorf("create body = %s", got.Body)
	}

	if v, err = volume.GetVolume(ctx, sc, volID); err != nil || v.Metadata["a"] != "1" {
		t.Fatalf("GetVolume = %+v, %v", v, err)
	}
	c.take()

	name := "renamed"
	if v, err = volume.UpdateVolume(ctx, sc, volID, volumes.UpdateOpts{Name: &name}); err != nil || v.Name != name {
		t.Fatalf("UpdateVolume = %+v, %v", v, err)
	}
	if got := c.only(t); got.Method != http.MethodPut || got.Body != `{"volume":{"name":"renamed"}}` {
		t.Errorf("update = %+v", got)
	}

	if _, err = volume.UpdateVolume(ctx, sc, volID, volume.MetadataUpdateOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := c.only(t); got.Body != `{"volume":{"metadata":{}}}` {
		t.Errorf("empty metadata body = %s; want the key sent", got.Body)
	}

	if err = volume.DeleteVolume(ctx, sc, volID, volumes.DeleteOpts{Cascade: true}); err != nil {
		t.Fatal(err)
	}
	if got := c.only(t); got.Method != http.MethodDelete || got.Query != "cascade=true" {
		t.Errorf("delete = %+v", got)
	}
}

func TestVolumeActions(t *testing.T) {
	c, sc := newCinder(t, "3.70", map[string]route{
		"POST /volumes/" + volID + "/action": fixed(202, ``),
	})
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		do   func() error
		body string
	}{
		{"extend", func() error { return volume.ExtendVolume(ctx, sc, volID, 20) },
			`{"os-extend":{"new_size":20}}`},
		{"reset status", func() error {
			return volume.ResetVolumeStatus(ctx, sc, volID, volume.ResetStatusOpts{Status: "available"})
		}, `{"os-reset_status":{"status":"available"}}`},
		{"reset attach status only", func() error {
			return volume.ResetVolumeStatus(ctx, sc, volID, volume.ResetStatusOpts{AttachStatus: "detached"})
		}, `{"os-reset_status":{"attach_status":"detached"}}`},
		{"retype", func() error {
			return volume.RetypeVolume(ctx, sc, volID, volumes.ChangeTypeOpts{NewType: "ssd", MigrationPolicy: volumes.MigrationPolicyOnDemand})
		}, `{"os-retype":{"migration_policy":"on-demand","new_type":"ssd"}}`},
		{"bootable", func() error { return volume.SetVolumeBootable(ctx, sc, volID, true) },
			`{"os-set_bootable":{"bootable":true}}`},
		{"migrate", func() error {
			return volume.MigrateVolume(ctx, sc, volID, volume.MigrateOpts{Host: "node-1@lvm#pool", LockVolume: true})
		}, `{"os-migrate_volume":{"force_host_copy":false,"host":"node-1@lvm#pool","lock_volume":true}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.do(); err != nil {
				t.Fatal(err)
			}
			got := c.only(t)
			if got.Method != http.MethodPost || got.Path != "/volumes/"+volID+"/action" || got.Body != tc.body {
				t.Errorf("request = %+v; want body %s", got, tc.body)
			}
			if got.Version != "volume 3.70" {
				t.Errorf("microversion header = %q", got.Version)
			}
		})
	}
}

func TestResetStatusOpts_NothingToReset(t *testing.T) {
	c, sc := newCinder(t, "3.70", nil)
	err := volume.ResetVolumeStatus(context.Background(), sc, volID, volume.ResetStatusOpts{})
	if !errors.Is(err, volume.ErrNothingToReset) {
		t.Fatalf("err = %v; want ErrNothingToReset", err)
	}
	if got := c.take(); len(got) != 0 {
		t.Errorf("sent %+v; want nothing", got)
	}
}

// Every call names what it was doing and the ID it was given, and keeps
// gophercloud's error reachable.
func TestErrorsNameTheCall(t *testing.T) {
	_, sc := newCinder(t, "3.70", nil)
	ctx := context.Background()
	for _, tc := range []struct {
		want string
		err  error
	}{
		{"listing volumes: ", func() error { _, err := volume.ListVolumes(ctx, sc, volumes.ListOpts{}, 0); return err }()},
		{"getting volume " + volID + ": ", func() error { _, err := volume.GetVolume(ctx, sc, volID); return err }()},
		{"creating volume: ", func() error { _, err := volume.CreateVolume(ctx, sc, volumes.CreateOpts{Size: 1}, nil); return err }()},
		{"updating volume " + volID + ": ", func() error {
			_, err := volume.UpdateVolume(ctx, sc, volID, volume.MetadataUpdateOpts{})
			return err
		}()},
		{"deleting volume " + volID + ": ", volume.DeleteVolume(ctx, sc, volID, nil)},
		{"extending volume " + volID + " to 5 GiB: ", volume.ExtendVolume(ctx, sc, volID, 5)},
		{"resetting status of volume " + volID + ": ", volume.ResetVolumeStatus(ctx, sc, volID, volume.ResetStatusOpts{Status: "error"})},
		{"retyping volume " + volID + ": ", volume.RetypeVolume(ctx, sc, volID, volumes.ChangeTypeOpts{NewType: "x"})},
		{"setting bootable flag on volume " + volID + ": ", volume.SetVolumeBootable(ctx, sc, volID, false)},
		{"migrating volume " + volID + ": ", volume.MigrateVolume(ctx, sc, volID, volume.MigrateOpts{Host: "h"})},
		{"listing volume attachments: ", func() error {
			_, err := volume.ListAttachments(ctx, sc, attachments.ListOpts{}, 0)
			return err
		}()},
		{"getting volume attachment " + atID + ": ", func() error { _, err := volume.GetAttachment(ctx, sc, atID); return err }()},
		{"creating attachment for volume " + volID + ": ", func() error {
			_, err := volume.CreateAttachment(ctx, sc, attachments.CreateOpts{VolumeUUID: volID, InstanceUUID: srvID})
			return err
		}()},
		{"updating volume attachment " + atID + ": ", func() error {
			_, err := volume.UpdateAttachment(ctx, sc, atID, attachments.UpdateOpts{Connector: map[string]any{"ip": "192.0.2.10"}})
			return err
		}()},
		{"deleting volume attachment " + atID + ": ", volume.DeleteAttachment(ctx, sc, atID)},
		{"completing volume attachment " + atID + ": ", volume.CompleteAttachment(ctx, sc, atID)},
	} {
		if tc.err == nil || !strings.HasPrefix(tc.err.Error(), tc.want) {
			t.Errorf("err = %v; want prefix %q", tc.err, tc.want)
			continue
		}
		if !gophercloud.ResponseCodeIs(tc.err, http.StatusNotFound) {
			t.Errorf("%q: gophercloud's 404 is not reachable", tc.want)
		}
	}
}

func TestAttachments(t *testing.T) {
	at := `{"attachment":{"id":"` + atID + `","volume_id":"` + volID + `","instance":"` + srvID + `","status":"reserved"}}`
	c, sc := newCinder(t, volume.AttachModeMicroversion, map[string]route{
		"GET /attachments/detail":               fixed(200, `{"attachments":[{"id":"x1"},{"id":"x2"}]}`),
		"GET /attachments/" + atID:              fixed(200, at),
		"POST /attachments":                     fixed(200, at),
		"PUT /attachments/" + atID:              fixed(200, at),
		"DELETE /attachments/" + atID:           fixed(200, `{}`),
		"POST /attachments/" + atID + "/action": fixed(204, ``),
	})
	ctx := context.Background()

	all, err := volume.ListAttachments(ctx, sc, attachments.ListOpts{AllTenants: true, VolumeID: volID}, 1)
	if err != nil || len(all) != 1 || all[0].ID != "x1" {
		t.Fatalf("ListAttachments = %+v, %v", all, err)
	}
	if got := c.only(t); got.Query != "all_tenants=true&volume_id="+volID || got.Version != "volume 3.54" {
		t.Errorf("list = %+v", got)
	}

	if a, err := volume.GetAttachment(ctx, sc, atID); err != nil || a.Status != "reserved" {
		t.Fatalf("GetAttachment = %+v, %v", a, err)
	}
	c.take()

	if _, err = volume.CreateAttachment(ctx, sc, attachments.CreateOpts{VolumeUUID: volID, InstanceUUID: srvID, Mode: "ro"}); err != nil {
		t.Fatal(err)
	}
	want := `{"attachment":{"instance_uuid":"` + srvID + `","mode":"ro","volume_uuid":"` + volID + `"}}`
	if got := c.only(t); got.Body != want {
		t.Errorf("create body = %s; want %s", got.Body, want)
	}

	if _, err = volume.UpdateAttachment(ctx, sc, atID, attachments.UpdateOpts{Connector: map[string]any{"ip": "192.0.2.10"}}); err != nil {
		t.Fatal(err)
	}
	if got := c.only(t); got.Method != http.MethodPut || got.Body != `{"attachment":{"connector":{"ip":"192.0.2.10"}}}` {
		t.Errorf("update = %+v", got)
	}

	if err = volume.CompleteAttachment(ctx, sc, atID); err != nil {
		t.Fatal(err)
	}
	if got := c.only(t); got.Path != "/attachments/"+atID+"/action" || got.Body != `{"os-complete":null}` {
		t.Errorf("complete = %+v", got)
	}

	if err = volume.DeleteAttachment(ctx, sc, atID); err != nil {
		t.Fatal(err)
	}
	if got := c.only(t); got.Method != http.MethodDelete {
		t.Errorf("delete = %+v", got)
	}
}
