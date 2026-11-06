package compute_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/koc/pkg/openstack/compute"
)

const migrationID = "7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d"

func TestListMigrations(t *testing.T) {
	fs, sc := nova(t, "2.80")
	var queries []string
	fs.Mux.HandleFunc("/os-migrations", func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("marker") == "" || r.URL.Query().Get("marker") == "m0" {
			answer(w, http.StatusOK, `{"migrations":[{"id":1,"status":"completed","old_instance_type_id":1,"new_instance_type_id":null},{"id":2}],
			  "migrations_links":[{"rel":"next","href":"`+sc.Endpoint+`os-migrations?host=cmp-1&marker=m2"}]}`)
			return
		}
		answer(w, http.StatusOK, `{"migrations":[{"id":3}]}`)
	})
	ctx := context.Background()

	all, err := compute.ListMigrations(ctx, sc, compute.MigrationListOpts{Host: "cmp-1", MigrationType: "live-migration",
		InstanceUUID: serverID, CreatedSince: "2026-01-01T00:00:00Z"}, 0)
	if err != nil || len(all) != 3 || all[0].OldFlavorID.String() != "1" || all[0].NewFlavorID.String() != "" {
		t.Fatalf("= %+v, %v", all, err)
	}
	want := []string{
		"created-since=2026-01-01T00%3A00%3A00Z&host=cmp-1&instance_uuid=" + serverID + "&migration_type=live-migration",
		"host=cmp-1&marker=m2",
	}
	if strings.Join(queries, " ") != strings.Join(want, " ") {
		t.Errorf("queries = %q, want %q", queries, want)
	}

	// limit is a cap on the walk, never sent.
	queries = nil
	all, err = compute.ListMigrations(ctx, sc, compute.MigrationListOpts{}, 1)
	if err != nil || len(all) != 1 || len(queries) != 1 || queries[0] != "" {
		t.Fatalf("limit 1 = %+v, %v after %q", all, err, queries)
	}
}

func TestListMigrations_Error(t *testing.T) {
	fs, sc := nova(t, "2.93")
	fs.Mux.HandleFunc("/os-migrations", func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusBadRequest, `{"badRequest":{"code":400,"message":"Invalid input"}}`)
	})
	_, err := compute.ListMigrations(context.Background(), sc, compute.MigrationListOpts{}, 0)
	if err == nil || !strings.HasPrefix(err.Error(), "listing migrations: ") || !gophercloud.ResponseCodeIs(err, http.StatusBadRequest) {
		t.Fatalf("err = %v", err)
	}
}

func TestServerMigrations(t *testing.T) {
	fs, sc := nova(t, "2.80")
	var rec calls
	one := `{"id":7,"uuid":"` + migrationID + `","status":"running","server_uuid":"` + serverID + `","memory_total_bytes":1024,"memory_remaining_bytes":512}`
	fs.Mux.HandleFunc("/servers/"+serverID+"/migrations", rec.handler(func(_ *http.Request, _ string) (int, string) {
		return http.StatusOK, `{"migrations":[` + one + `]}`
	}))
	fs.Mux.HandleFunc("/servers/"+serverID+"/migrations/"+migrationID, rec.handler(func(r *http.Request, _ string) (int, string) {
		if r.Method == http.MethodDelete {
			return http.StatusAccepted, ""
		}
		return http.StatusOK, `{"migration":` + one + `}`
	}))
	fs.Mux.HandleFunc("/servers/"+serverID+"/migrations/"+migrationID+"/action", rec.handler(func(_ *http.Request, _ string) (int, string) {
		return http.StatusAccepted, ""
	}))
	ctx := context.Background()

	list, err := compute.ListServerMigrations(ctx, sc, serverID)
	if err != nil || len(list) != 1 || list[0].MemoryRemainingBytes != 512 {
		t.Fatalf("ListServerMigrations = %+v, %v", list, err)
	}
	if got := rec.only(t); got.Method != http.MethodGet || got.Version != "compute 2.80" {
		t.Errorf("list sent %+v", got)
	}

	m, err := compute.GetServerMigration(ctx, sc, serverID, migrationID)
	if err != nil || m.ID != 7 || m.ServerUUID != serverID {
		t.Fatalf("GetServerMigration = %+v, %v", m, err)
	}
	rec.only(t)

	if err := compute.AbortServerMigration(ctx, sc, serverID, migrationID); err != nil {
		t.Fatal(err)
	}
	if got := rec.only(t); got.Method != http.MethodDelete || got.Path != "/servers/"+serverID+"/migrations/"+migrationID {
		t.Errorf("abort sent %+v", got)
	}

	if err := compute.ForceCompleteServerMigration(ctx, sc, serverID, migrationID); err != nil {
		t.Fatal(err)
	}
	if got := rec.only(t); got.Method != http.MethodPost || got.Body != `{"force_complete":null}` {
		t.Errorf("force complete sent %+v", got)
	}
}

func TestServerMigrations_ErrorsNameBoth(t *testing.T) {
	fs, sc := nova(t, "2.93")
	notFound := func(w http.ResponseWriter, _ *http.Request) {
		answer(w, http.StatusNotFound, `{"itemNotFound":{"code":404,"message":"no such migration"}}`)
	}
	fs.Mux.HandleFunc("/servers/"+serverID+"/migrations", notFound)
	fs.Mux.HandleFunc("/servers/"+serverID+"/migrations/"+migrationID, notFound)
	fs.Mux.HandleFunc("/servers/"+serverID+"/migrations/"+migrationID+"/action", notFound)
	ctx := context.Background()
	_, listErr := compute.ListServerMigrations(ctx, sc, serverID)
	_, getErr := compute.GetServerMigration(ctx, sc, serverID, migrationID)
	for prefix, err := range map[string]error{
		"listing migrations of server " + serverID:                                  listErr,
		"showing migration " + migrationID + " of server " + serverID:               getErr,
		"aborting migration " + migrationID + " of server " + serverID:              compute.AbortServerMigration(ctx, sc, serverID, migrationID),
		"forcing completion of migration " + migrationID + " of server " + serverID: compute.ForceCompleteServerMigration(ctx, sc, serverID, migrationID),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), prefix+": ") || !gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
			t.Errorf("err = %v, want %q wrapping the 404", err, prefix)
		}
	}
}
