package volume_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/backups"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/qos"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumetypes"

	"github.com/ftarasenko/koc/pkg/openstack/volume"
)

const (
	snapID = "5d4c3b2a-1f0e-4d9c-8b7a-6f5e4d3c2b1a"
	bakID  = "7f6e5d4c-3b2a-4f1e-9d8c-7b6a5f4e3d2c"
	qosID  = "8a9b0c1d-2e3f-4a5b-8c6d-7e8f9a0b1c2d"
)

// want checks one recorded request against method, path, query and body.
func (c *cinder) want(t *testing.T, method, path, query, body string) {
	t.Helper()
	got := c.only(t)
	if got.Method != method || got.Path != path || got.Query != query || got.Body != body {
		t.Errorf("request = %+v; want %s %s?%s %s", got, method, path, query, body)
	}
	if got.Version != "volume 3.70" {
		t.Errorf("microversion header = %q", got.Version)
	}
}

func TestSnapshots(t *testing.T) {
	snap := `{"snapshot":{"id":"` + snapID + `","name":"s1","status":"available","metadata":{"a":"1"}}}`
	c, sc := newCinder(t, "3.70", map[string]route{
		"GET /snapshots": func(r *http.Request) (int, string) {
			if r.URL.Query().Get("marker") == "" {
				return 200, `{"snapshots":[{"id":"a"},{"id":"b"}],
					"snapshots_links":[{"rel":"next","href":"http://` + r.Host + `/snapshots?marker=b"}]}`
			}
			return 200, `{"snapshots":[{"id":"c"}]}`
		},
		"POST /snapshots":                        fixed(202, snap),
		"GET /snapshots/" + snapID:               fixed(200, snap),
		"PUT /snapshots/" + snapID:               fixed(200, snap),
		"PUT /snapshots/" + snapID + "/metadata": fixed(200, `{"metadata":{"b":"2"}}`),
		"PUT /snapshots/null-reply/metadata":     fixed(200, `{"metadata":null}`),
		"POST /snapshots/" + snapID + "/action":  fixed(202, ``),
		"DELETE /snapshots/" + snapID:            fixed(202, ``),
	})
	ctx := context.Background()
	p := "/snapshots/" + snapID

	all, err := volume.ListSnapshots(ctx, sc, snapshots.ListOpts{Name: "s1"}, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListSnapshots = %d, %v; want 3 across two pages", len(all), err)
	}
	if got := c.take(); got[0].Query != "name=s1" {
		t.Errorf("query = %q", got[0].Query)
	}
	if capped, err := volume.ListSnapshots(ctx, sc, snapshots.ListOpts{}, 1); err != nil || len(capped) != 1 {
		t.Fatalf("capped = %+v, %v", capped, err)
	}
	c.take()

	if s, err := volume.CreateSnapshot(ctx, sc, snapshots.CreateOpts{VolumeID: volID, Name: "s1"}); err != nil || s.ID != snapID {
		t.Fatalf("CreateSnapshot = %+v, %v", s, err)
	}
	c.want(t, http.MethodPost, "/snapshots", "", `{"snapshot":{"name":"s1","volume_id":"`+volID+`"}}`)

	if s, err := volume.GetSnapshot(ctx, sc, snapID); err != nil || s.Metadata["a"] != "1" {
		t.Fatalf("GetSnapshot = %+v, %v", s, err)
	}
	c.want(t, http.MethodGet, p, "", "")

	name := "s2"
	if _, err := volume.UpdateSnapshot(ctx, sc, snapID, snapshots.UpdateOpts{Name: &name}); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodPut, p, "", `{"snapshot":{"name":"s2"}}`)

	m, err := volume.ReplaceSnapshotMetadata(ctx, sc, snapID, map[string]any{"b": "2"})
	if err != nil || m["b"] != "2" {
		t.Fatalf("ReplaceSnapshotMetadata = %v, %v", m, err)
	}
	c.want(t, http.MethodPut, p+"/metadata", "", `{"metadata":{"b":"2"}}`)

	// A reply without a metadata object is not a panic.
	if m, err := volume.ReplaceSnapshotMetadata(ctx, sc, "null-reply", map[string]any{"b": "2"}); err != nil || m != nil {
		t.Fatalf("ReplaceSnapshotMetadata(null reply) = %v, %v", m, err)
	}
	c.take()

	if err := volume.ResetSnapshotStatus(ctx, sc, snapID, "error"); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodPost, p+"/action", "", `{"os-reset_status":{"status":"error"}}`)

	if err := volume.DeleteSnapshot(ctx, sc, snapID); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodDelete, p, "", "")
}

func TestBackups(t *testing.T) {
	bak := `{"backup":{"id":"` + bakID + `","name":"b1","status":"available"}}`
	c, sc := newCinder(t, "3.70", map[string]route{
		"GET /backups/detail": func(r *http.Request) (int, string) {
			if r.URL.Query().Get("marker") == "" {
				return 200, `{"backups":[{"id":"a"},{"id":"b"}],
					"backups_links":[{"rel":"next","href":"http://` + r.Host + `/backups/detail?marker=b"}]}`
			}
			return 200, `{"backups":[{"id":"c"}]}`
		},
		"POST /backups":                       fixed(202, bak),
		"GET /backups/" + bakID:               fixed(200, bak),
		"PUT /backups/" + bakID:               fixed(200, bak),
		"POST /backups/" + bakID + "/action":  fixed(202, ``),
		"POST /backups/" + bakID + "/restore": fixed(202, `{"restore":{"backup_id":"`+bakID+`","volume_id":"`+volID+`","volume_name":"v"}}`),
		"DELETE /backups/" + bakID:            fixed(202, ``),
	})
	ctx := context.Background()
	p := "/backups/" + bakID

	all, err := volume.ListBackups(ctx, sc, backups.ListOpts{Name: "b1", VolumeID: volID}, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListBackups = %d, %v; want 3 across two pages", len(all), err)
	}
	if got := c.take(); got[0].Path != "/backups/detail" || got[0].Query != "name=b1&volume_id="+volID {
		t.Errorf("first request = %+v; want the filters on the detail view", got[0])
	}
	if capped, err := volume.ListBackups(ctx, sc, backups.ListOpts{}, 2); err != nil || len(capped) != 2 {
		t.Fatalf("capped = %+v, %v", capped, err)
	}
	c.take()

	if b, err := volume.CreateBackup(ctx, sc, backups.CreateOpts{VolumeID: volID, Name: "b1"}); err != nil || b.ID != bakID {
		t.Fatalf("CreateBackup = %+v, %v", b, err)
	}
	c.want(t, http.MethodPost, "/backups", "", `{"backup":{"name":"b1","volume_id":"`+volID+`"}}`)

	if b, err := volume.GetBackup(ctx, sc, bakID); err != nil || b.Name != "b1" {
		t.Fatalf("GetBackup = %+v, %v", b, err)
	}
	c.want(t, http.MethodGet, p, "", "")

	name := "b2"
	if _, err := volume.UpdateBackup(ctx, sc, bakID, backups.UpdateOpts{Name: &name, Metadata: map[string]string{"k": "v"}}); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodPut, p, "", `{"backup":{"metadata":{"k":"v"},"name":"b2"}}`)

	if err := volume.ResetBackupStatus(ctx, sc, bakID, "error"); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodPost, p+"/action", "", `{"os-reset_status":{"status":"error"}}`)

	r, err := volume.RestoreBackup(ctx, sc, bakID, backups.RestoreOpts{VolumeID: volID})
	if err != nil || r.VolumeID != volID {
		t.Fatalf("RestoreBackup = %+v, %v", r, err)
	}
	c.want(t, http.MethodPost, p+"/restore", "", `{"restore":{"volume_id":"`+volID+`"}}`)

	if err := volume.DeleteBackup(ctx, sc, bakID); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodDelete, p, "", "")
}

// The body is wrapped once: also when a later gophercloud wraps it itself.
func TestWrapBackupBody(t *testing.T) {
	once := volume.WrapBackupBody(map[string]any{"name": "x"})
	if _, ok := once["backup"].(map[string]any); !ok {
		t.Fatalf("body = %#v; want it under \"backup\"", once)
	}
	twice := volume.WrapBackupBody(once)
	if _, double := twice["backup"].(map[string]any)["backup"]; double {
		t.Errorf("body was wrapped twice: %#v", twice)
	}
}

func TestVolumeTypes(t *testing.T) {
	vt := `{"volume_type":{"id":"` + typeID + `","name":"ssd","is_public":true}}`
	c, sc := newCinder(t, "3.70", map[string]route{
		"GET /types":                                 fixed(200, `{"volume_types":[{"id":"`+typeID+`","name":"ssd"}]}`),
		"POST /types":                                fixed(200, vt),
		"GET /types/" + typeID:                       fixed(200, vt),
		"PUT /types/" + typeID:                       fixed(200, vt),
		"POST /types/" + typeID + "/extra_specs":     fixed(200, `{"extra_specs":{"volume_backend_name":"lvm"}}`),
		"DELETE /types/" + typeID + "/extra_specs/k": fixed(202, ``),
		"DELETE /types/" + typeID:                    fixed(202, ``),
	})
	ctx := context.Background()
	p := "/types/" + typeID

	all, err := volume.ListVolumeTypes(ctx, sc, volumetypes.ListOpts{Name: "ssd"})
	if err != nil || len(all) != 1 || all[0].ID != typeID {
		t.Fatalf("ListVolumeTypes = %+v, %v", all, err)
	}
	c.want(t, http.MethodGet, "/types", "is_public=None&name=ssd", "")

	if _, err := volume.CreateVolumeType(ctx, sc, volumetypes.CreateOpts{Name: "ssd"}); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodPost, "/types", "", `{"volume_type":{"name":"ssd"}}`)

	if v, err := volume.GetVolumeType(ctx, sc, typeID); err != nil || v.Name != "ssd" {
		t.Fatalf("GetVolumeType = %+v, %v", v, err)
	}
	c.want(t, http.MethodGet, p, "", "")

	name := "fast"
	if _, err := volume.UpdateVolumeType(ctx, sc, typeID, volumetypes.UpdateOpts{Name: &name}); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodPut, p, "", `{"volume_type":{"name":"fast"}}`)

	specs, err := volume.SetVolumeTypeExtraSpecs(ctx, sc, typeID, map[string]string{"volume_backend_name": "lvm"})
	if err != nil || specs["volume_backend_name"] != "lvm" {
		t.Fatalf("SetVolumeTypeExtraSpecs = %v, %v", specs, err)
	}
	c.want(t, http.MethodPost, p+"/extra_specs", "", `{"extra_specs":{"volume_backend_name":"lvm"}}`)

	if err := volume.DeleteVolumeTypeExtraSpec(ctx, sc, typeID, "k"); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodDelete, p+"/extra_specs/k", "", "")

	if err := volume.DeleteVolumeType(ctx, sc, typeID); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodDelete, p, "", "")
}

func TestQoS(t *testing.T) {
	spec := `{"qos_specs":{"id":"` + qosID + `","name":"gold","consumer":"back-end","specs":{"read_iops_sec":"100"}}}`
	c, sc := newCinder(t, "3.70", map[string]route{
		"GET /qos-specs": func(r *http.Request) (int, string) {
			if r.URL.Query().Get("marker") == "" {
				return 200, `{"qos_specs":[{"id":"a"},{"id":"b"}],
					"qos_specs_links":[{"rel":"next","href":"http://` + r.Host + `/qos-specs?marker=b"}]}`
			}
			return 200, `{"qos_specs":[{"id":"c"}]}`
		},
		"GET /qos-specs/" + qosID + "/associations": fixed(200, `{"qos_associations":[{"id":"`+typeID+`","name":"ssd","association_type":"volume_type"}]}`),
		"POST /qos-specs":                               fixed(200, spec),
		"GET /qos-specs/" + qosID:                       fixed(200, spec),
		"PUT /qos-specs/" + qosID:                       fixed(200, `{"qos_specs":{"write_iops_sec":"50"}}`),
		"PUT /qos-specs/" + qosID + "/delete_keys":      fixed(202, ``),
		"GET /qos-specs/" + qosID + "/associate":        fixed(202, ``),
		"GET /qos-specs/" + qosID + "/disassociate":     fixed(202, ``),
		"GET /qos-specs/" + qosID + "/disassociate_all": fixed(202, ``),
		"DELETE /qos-specs/" + qosID:                    fixed(202, ``),
	})
	ctx := context.Background()
	p := "/qos-specs/" + qosID

	all, err := volume.ListQoS(ctx, sc, qos.ListOpts{}, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListQoS = %d, %v; want 3 across two pages", len(all), err)
	}
	c.take()
	if capped, err := volume.ListQoS(ctx, sc, qos.ListOpts{Limit: 1}, 1); err != nil || len(capped) != 1 {
		t.Fatalf("capped = %+v, %v", capped, err)
	}
	c.want(t, http.MethodGet, "/qos-specs", "limit=1", "")

	as, err := volume.ListQoSAssociations(ctx, sc, qosID)
	if err != nil || len(as) != 1 || as[0].Name != "ssd" {
		t.Fatalf("ListQoSAssociations = %+v, %v", as, err)
	}
	c.want(t, http.MethodGet, p+"/associations", "", "")

	q, err := volume.CreateQoS(ctx, sc, qos.CreateOpts{Name: "gold", Consumer: qos.ConsumerBack, Specs: map[string]string{"read_iops_sec": "100"}})
	if err != nil || q.ID != qosID {
		t.Fatalf("CreateQoS = %+v, %v", q, err)
	}
	c.want(t, http.MethodPost, "/qos-specs", "", `{"qos_specs":{"consumer":"back-end","name":"gold","read_iops_sec":"100"}}`)

	if q, err := volume.GetQoS(ctx, sc, qosID); err != nil || q.Specs["read_iops_sec"] != "100" {
		t.Fatalf("GetQoS = %+v, %v", q, err)
	}
	c.want(t, http.MethodGet, p, "", "")

	m, err := volume.SetQoSSpecs(ctx, sc, qosID, map[string]string{"write_iops_sec": "50"})
	if err != nil || m["write_iops_sec"] != "50" {
		t.Fatalf("SetQoSSpecs = %v, %v", m, err)
	}
	c.want(t, http.MethodPut, p, "", `{"qos_specs":{"write_iops_sec":"50"}}`)

	if err := volume.DeleteQoSKeys(ctx, sc, qosID, []string{"read_iops_sec"}); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodPut, p+"/delete_keys", "", `{"keys":["read_iops_sec"]}`)

	if err := volume.AssociateQoS(ctx, sc, qosID, typeID); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodGet, p+"/associate", "vol_type_id="+typeID, "")

	if err := volume.DisassociateQoS(ctx, sc, qosID, typeID); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodGet, p+"/disassociate", "vol_type_id="+typeID, "")

	if err := volume.DisassociateAllQoS(ctx, sc, qosID); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodGet, p+"/disassociate_all", "", "")

	if err := volume.DeleteQoS(ctx, sc, qosID, true); err != nil {
		t.Fatal(err)
	}
	c.want(t, http.MethodDelete, p, "force=true", "")
}

// Every call names what it was doing and the ID it was given, and keeps
// gophercloud's 404 reachable.
func TestResourceErrorsNameTheCall(t *testing.T) {
	_, sc := newCinder(t, "3.70", nil)
	ctx := context.Background()
	for _, tc := range []struct {
		want string
		err  error
	}{
		{"listing snapshots: ", second(volume.ListSnapshots(ctx, sc, snapshots.ListOpts{}, 0))},
		{"getting snapshot " + snapID + ": ", second(volume.GetSnapshot(ctx, sc, snapID))},
		{"creating snapshot: ", second(volume.CreateSnapshot(ctx, sc, snapshots.CreateOpts{VolumeID: volID}))},
		{"updating snapshot " + snapID + ": ", second(volume.UpdateSnapshot(ctx, sc, snapID, snapshots.UpdateOpts{}))},
		{"updating properties of snapshot " + snapID + ": ", second(volume.ReplaceSnapshotMetadata(ctx, sc, snapID, nil))},
		{"resetting the status of snapshot " + snapID + ": ", volume.ResetSnapshotStatus(ctx, sc, snapID, "error")},
		{"deleting snapshot " + snapID + ": ", volume.DeleteSnapshot(ctx, sc, snapID)},

		{"listing backups: ", second(volume.ListBackups(ctx, sc, backups.ListOpts{}, 0))},
		{"getting backup " + bakID + ": ", second(volume.GetBackup(ctx, sc, bakID))},
		{"creating backup: ", second(volume.CreateBackup(ctx, sc, backups.CreateOpts{VolumeID: volID}))},
		{"updating backup " + bakID + ": ", second(volume.UpdateBackup(ctx, sc, bakID, backups.UpdateOpts{}))},
		{"resetting the status of backup " + bakID + ": ", volume.ResetBackupStatus(ctx, sc, bakID, "error")},
		{"deleting backup " + bakID + ": ", volume.DeleteBackup(ctx, sc, bakID)},
		{"restoring backup " + bakID + ": ", second(volume.RestoreBackup(ctx, sc, bakID, backups.RestoreOpts{}))},

		{"listing volume types: ", second(volume.ListVolumeTypes(ctx, sc, volumetypes.ListOpts{}))},
		{"getting volume type " + typeID + ": ", second(volume.GetVolumeType(ctx, sc, typeID))},
		{"creating volume type: ", second(volume.CreateVolumeType(ctx, sc, volumetypes.CreateOpts{Name: "x"}))},
		{"updating volume type " + typeID + ": ", second(volume.UpdateVolumeType(ctx, sc, typeID, volumetypes.UpdateOpts{}))},
		{"deleting volume type " + typeID + ": ", volume.DeleteVolumeType(ctx, sc, typeID)},
		{"setting extra-specs on volume type " + typeID + ": ", second(volume.SetVolumeTypeExtraSpecs(ctx, sc, typeID, map[string]string{"k": "v"}))},
		{`removing extra-spec "k" from volume type ` + typeID + ": ", volume.DeleteVolumeTypeExtraSpec(ctx, sc, typeID, "k")},

		{"listing volume QoS specifications: ", second(volume.ListQoS(ctx, sc, qos.ListOpts{}, 0))},
		{"listing the associations of volume QoS specification " + qosID + ": ", second(volume.ListQoSAssociations(ctx, sc, qosID))},
		{"showing volume QoS specification " + qosID + ": ", second(volume.GetQoS(ctx, sc, qosID))},
		{`creating volume QoS specification "gold": `, second(volume.CreateQoS(ctx, sc, qos.CreateOpts{Name: "gold"}))},
		{"deleting volume QoS specification " + qosID + ": ", volume.DeleteQoS(ctx, sc, qosID, false)},
		{"updating QoS specification " + qosID + ": ", second(volume.SetQoSSpecs(ctx, sc, qosID, map[string]string{"k": "v"}))},
		{"removing properties from QoS specification " + qosID + ": ", volume.DeleteQoSKeys(ctx, sc, qosID, []string{"k"})},
		{"associating QoS specification " + qosID + " with volume type " + typeID + ": ", volume.AssociateQoS(ctx, sc, qosID, typeID)},
		{"disassociating volume type " + typeID + " from QoS specification " + qosID + ": ", volume.DisassociateQoS(ctx, sc, qosID, typeID)},
		{"disassociating every volume type from QoS specification " + qosID + ": ", volume.DisassociateAllQoS(ctx, sc, qosID)},
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

func second[T any](_ T, err error) error { return err }
