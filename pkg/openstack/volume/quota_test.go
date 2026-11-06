package volume_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/quotasets"

	"github.com/ftarasenko/koc/pkg/openstack"
	"github.com/ftarasenko/koc/pkg/openstack/volume"
)

const projID = "5d4c3b2a-1f0e-4d9c-8b7a-6f5e4d3c2b1a"

func TestQuotaSets(t *testing.T) {
	set := `{"quota_set":{"id":"` + projID + `","volumes":10,"gigabytes":100,"per_volume_gigabytes":-1,
		"gigabytes_ssd":50,"volumes_ssd":4,"snapshots_ssd":2,"volumes_":1,"backup_gigabytes":9,"volumes_odd":"x"}}`
	c, sc := newCinder(t, "3.70", map[string]route{
		"GET /os-quota-sets/" + projID:               fixed(200, set),
		"GET /os-quota-sets/" + projID + "/defaults": fixed(200, `{"quota_set":{"volumes":5}}`),
		"PUT /os-quota-sets/" + projID:               fixed(200, set),
		"GET /os-quota-sets/empty":                   fixed(200, `{}`),
	})
	ctx := context.Background()

	qs, err := volume.GetQuotaSet(ctx, sc, projID)
	if err != nil || qs.Volumes != 10 || qs.Gigabytes != 100 {
		t.Fatalf("GetQuotaSet = %+v, %v", qs, err)
	}
	want := map[string]int{"gigabytes_ssd": 50, "volumes_ssd": 4, "snapshots_ssd": 2}
	if len(qs.PerVolumeType) != len(want) {
		t.Errorf("PerVolumeType = %v; want %v", qs.PerVolumeType, want)
	}
	for k, v := range want {
		if qs.PerVolumeType[k] != v {
			t.Errorf("PerVolumeType[%s] = %d; want %d", k, qs.PerVolumeType[k], v)
		}
	}
	c.want(t, http.MethodGet, "/os-quota-sets/"+projID, "", "")

	qs, err = volume.GetDefaultQuotaSet(ctx, sc, projID)
	if err != nil || qs.Volumes != 5 || qs.PerVolumeType != nil {
		t.Fatalf("GetDefaultQuotaSet = %+v, %v", qs, err)
	}
	c.want(t, http.MethodGet, "/os-quota-sets/"+projID+"/defaults", "", "")

	n := 3
	if qs, err = volume.UpdateQuotaSet(ctx, sc, projID, quotasets.UpdateOpts{Volumes: &n, Force: true}); err != nil || qs.PerVolumeType["volumes_ssd"] != 4 {
		t.Fatalf("UpdateQuotaSet = %+v, %v", qs, err)
	}
	c.want(t, http.MethodPut, "/os-quota-sets/"+projID, "", `{"quota_set":{"force":true,"volumes":3}}`)

	_, err = volume.GetQuotaSet(ctx, sc, "empty")
	if !errors.Is(err, openstack.ErrNoObject) ||
		err.Error() != `showing volume quotas for project "empty": the server returned success but no object` {
		t.Errorf("empty reply: %v", err)
	}
	c.take()
	for _, tc := range []struct {
		err  error
		text string
	}{
		{second(volume.GetQuotaSet(ctx, sc, "nope")), `showing volume quotas for project "nope": `},
		{second(volume.GetDefaultQuotaSet(ctx, sc, "nope")), `showing default volume quotas for project "nope": `},
		{second(volume.UpdateQuotaSet(ctx, sc, "nope", quotasets.UpdateOpts{Volumes: &n})), `setting volume quotas for project "nope": `},
	} {
		if !gophercloud.ResponseCodeIs(tc.err, http.StatusNotFound) || !strings.HasPrefix(tc.err.Error(), tc.text) {
			t.Errorf("error = %v; want %q and the 404", tc.err, tc.text)
		}
	}
}

func TestQuotaSets_MalformedReply(t *testing.T) {
	_, sc := newCinder(t, "3.70", map[string]route{
		"GET /os-quota-sets/" + projID: fixed(200, `{"quota_set":{"volumes":"many"}}`),
	})
	if _, err := volume.GetQuotaSet(context.Background(), sc, projID); err == nil {
		t.Error("a non-integer volumes quota decoded")
	}
}

func TestIsPerVolumeTypeQuota(t *testing.T) {
	for key, want := range map[string]bool{
		"gigabytes_ssd": true, "volumes_lvm-1": true, "snapshots_x": true,
		"gigabytes": false, "volumes_": false, "per_volume_gigabytes": false,
		"backup_gigabytes": false, "backups_ssd": false,
	} {
		if got := volume.IsPerVolumeTypeQuota(key); got != want {
			t.Errorf("IsPerVolumeTypeQuota(%q) = %v; want %v", key, got, want)
		}
	}
}

func TestLimitsAndAvailabilityZones(t *testing.T) {
	limits := `{"limits":{"absolute":{"maxTotalVolumes":10,"totalVolumesUsed":3},"rate":[]}}`
	c, sc := newCinder(t, "3.70", map[string]route{
		"GET /limits":               sequence(limits, `{}`),
		"GET /os-availability-zone": sequence(`{"availabilityZoneInfo":[{"zoneName":"nova","zoneState":{"available":true}}]}`, `[`),
	})
	ctx := context.Background()

	if l, err := volume.GetLimits(ctx, sc); err != nil || l.Absolute.MaxTotalVolumes != 10 {
		t.Fatalf("GetLimits = %+v, %v", l, err)
	}
	c.want(t, http.MethodGet, "/limits", "", "")
	if _, err := volume.GetLimits(ctx, sc); !errors.Is(err, openstack.ErrNoObject) ||
		err.Error() != "getting volume limits: the server returned success but no object" {
		t.Errorf("empty reply: %v", err)
	}
	c.take()

	zones, err := volume.ListAvailabilityZones(ctx, sc)
	if err != nil || len(zones) != 1 || zones[0].ZoneName != "nova" || !zones[0].ZoneState.Available {
		t.Fatalf("ListAvailabilityZones = %+v, %v", zones, err)
	}
	c.want(t, http.MethodGet, "/os-availability-zone", "", "")
	if _, err := volume.ListAvailabilityZones(ctx, sc); err == nil ||
		!strings.HasPrefix(err.Error(), "listing volume availability zones: ") {
		t.Errorf("malformed reply: %v", err)
	}
}

func TestLimitsAndAvailabilityZones_Errors(t *testing.T) {
	_, sc := newCinder(t, "3.70", nil)
	ctx := context.Background()
	if _, err := volume.GetLimits(ctx, sc); !gophercloud.ResponseCodeIs(err, http.StatusNotFound) ||
		!strings.HasPrefix(err.Error(), "getting volume limits: ") {
		t.Errorf("GetLimits: %v", err)
	}
	if _, err := volume.ListAvailabilityZones(ctx, sc); !gophercloud.ResponseCodeIs(err, http.StatusNotFound) ||
		!strings.HasPrefix(err.Error(), "listing volume availability zones: ") {
		t.Errorf("ListAvailabilityZones: %v", err)
	}
}
