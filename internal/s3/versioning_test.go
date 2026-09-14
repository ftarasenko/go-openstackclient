package s3

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A bucket that was never configured answers an empty document, which must be
// normalised rather than handed to the caller as an empty string.
func TestGetBucketVersioning(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"enabled", `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`, VersioningEnabled},
		{"suspended", `<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration>`, VersioningSuspended},
		{"never configured", `<VersioningConfiguration/>`, VersioningUnversioned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hasParam bool
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				hasParam = r.URL.Query().Has("versioning")
				_, _ = fmt.Fprint(w, tc.body)
			})

			got, err := c.GetBucketVersioning(context.Background(), "b")
			if err != nil {
				t.Fatal(err)
			}
			if !hasParam {
				t.Error("the request did not carry ?versioning")
			}
			if got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSetBucketVersioning(t *testing.T) {
	var gotMethod, gotBody, gotMD5 string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertSigned(t, r)
		body, _ := io.ReadAll(r.Body)
		gotMethod, gotBody, gotMD5 = r.Method, string(body), r.Header.Get("Content-MD5")
		w.WriteHeader(http.StatusOK)
	})

	if err := c.SetBucketVersioning(context.Background(), "b", VersioningEnabled); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	want := "<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>"
	if gotBody != want {
		t.Errorf("body = %q, want %q", gotBody, want)
	}
	if gotMD5 == "" {
		t.Error("Content-MD5 was not sent")
	}
}

// S3 has no call that returns a bucket to never-versioned, so the constant that
// means it must not be accepted here.
func TestSetBucketVersioningRejectsAnImpossibleState(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an invalid status must be refused before any request")
		w.WriteHeader(http.StatusOK)
	})

	if err := c.SetBucketVersioning(context.Background(), "b", VersioningUnversioned); err == nil {
		t.Fatal("Unversioned was accepted as a settable status")
	}
}

// A versioned listing pages on a key marker plus a version-id marker, and its
// <Version> and <DeleteMarker> elements have to be presented together per key,
// newest first, rather than as two separate runs.
func TestListObjectVersionsPagesAndOrders(t *testing.T) {
	var queries []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		queries = append(queries, q.Get("key-marker")+"/"+q.Get("version-id-marker"))
		if !q.Has("versions") {
			t.Error("the request did not carry ?versions")
		}
		if q.Get("key-marker") == "" {
			_, _ = fmt.Fprint(w, `<ListVersionsResult><IsTruncated>true</IsTruncated>
				<NextKeyMarker>dump</NextKeyMarker><NextVersionIdMarker>v1</NextVersionIdMarker>
				<Version><Key>dump</Key><VersionId>v2</VersionId><IsLatest>true</IsLatest>
					<Size>20</Size><LastModified>2026-09-14T10:00:00.000Z</LastModified></Version>
				<DeleteMarker><Key>dump</Key><VersionId>v3</VersionId>
					<LastModified>2026-09-14T11:00:00.000Z</LastModified></DeleteMarker>
				</ListVersionsResult>`)
			return
		}
		_, _ = fmt.Fprint(w, `<ListVersionsResult><IsTruncated>false</IsTruncated>
			<Version><Key>dump</Key><VersionId>v1</VersionId><Size>10</Size>
				<LastModified>2026-09-14T09:00:00.000Z</LastModified></Version>
			</ListVersionsResult>`)
	})

	objs, err := c.ListObjects(context.Background(), "b", ListOptions{Versions: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || queries[1] != "dump/v1" {
		t.Errorf("requests = %v, want the second to carry both markers", queries)
	}
	if len(objs) != 3 {
		t.Fatalf("entries = %d, want 3", len(objs))
	}
	// Within a page: same key, newest first — the delete marker (11:00) before
	// the version it hides (10:00).
	if !objs[0].DeleteMarker || objs[0].VersionID != "v3" {
		t.Errorf("first entry = %+v, want the newest delete marker", objs[0])
	}
	if objs[1].VersionID != "v2" || !objs[1].IsLatest {
		t.Errorf("second entry = %+v, want version v2", objs[1])
	}
	if objs[2].VersionID != "v1" || objs[2].Size != 10 {
		t.Errorf("third entry = %+v, want version v1 from the second page", objs[2])
	}
}

// A delimited listing reports subtrees as CommonPrefixes, which is what turns a
// flat keyspace into one directory level.
func TestListObjectsWithADelimiter(t *testing.T) {
	var gotDelimiter string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotDelimiter = r.URL.Query().Get("delimiter")
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<CommonPrefixes><Prefix>2026/</Prefix></CommonPrefixes>
			<CommonPrefixes><Prefix>2025/</Prefix></CommonPrefixes>
			<Contents><Key>latest.sql.gz</Key><Size>7</Size></Contents>
			</ListBucketResult>`)
	})

	objs, err := c.ListObjects(context.Background(), "b", ListOptions{Delimiter: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if gotDelimiter != "/" {
		t.Errorf("delimiter = %q, want /", gotDelimiter)
	}
	if len(objs) != 3 {
		t.Fatalf("entries = %d, want 2 prefixes and 1 key", len(objs))
	}
	// Subtrees first, so a delimited listing reads like a directory.
	if !objs[0].IsPrefix || objs[0].Key != "2026/" {
		t.Errorf("first entry = %+v, want the 2026/ prefix", objs[0])
	}
	if !objs[1].IsPrefix {
		t.Errorf("second entry = %+v, want a prefix", objs[1])
	}
	if objs[2].IsPrefix || objs[2].Key != "latest.sql.gz" {
		t.Errorf("third entry = %+v, want the key at this level", objs[2])
	}
}

// --limit counts prefixes as well as keys: they are both entries the caller
// asked to see at most N of.
func TestListObjectsLimitCountsPrefixes(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<CommonPrefixes><Prefix>a/</Prefix></CommonPrefixes>
			<CommonPrefixes><Prefix>b/</Prefix></CommonPrefixes>
			<Contents><Key>c</Key></Contents>
			</ListBucketResult>`)
	})

	objs, err := c.ListObjects(context.Background(), "b", ListOptions{Delimiter: "/", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Errorf("entries = %d, want the limit honoured", len(objs))
	}
}

func TestHeadBucket(t *testing.T) {
	var gotMethod, gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	})

	if err := c.HeadBucket(context.Background(), "db-backups"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodHead || gotPath != "/db-backups" {
		t.Errorf("request = %s %s, want HEAD /db-backups", gotMethod, gotPath)
	}
}

// A HEAD reply has no body, so a missing bucket is a bare 404 and IsNotFound
// has to recognise it from the status alone.
func TestHeadBucketNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	err := c.HeadBucket(context.Background(), "absent")
	if err == nil || !IsNotFound(err) {
		t.Errorf("err = %v, want a recognised not-found", err)
	}
}

// A version ID must reach the request as a query parameter on the object's URL.
func TestVersionedObjectAddressing(t *testing.T) {
	var queries []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("versionId"))
		w.Header().Set("x-amz-version-id", "v9")
		w.WriteHeader(http.StatusOK)
	})

	info, err := c.HeadObject(context.Background(), "b", "k", "v9")
	if err != nil {
		t.Fatal(err)
	}
	if info.VersionID != "v9" {
		t.Errorf("VersionID = %q, want v9 from the response header", info.VersionID)
	}
	if err := c.DeleteObject(context.Background(), "b", "k", "v9"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetObject(context.Background(), "b", "k", "v9", io.Discard); err != nil {
		t.Fatal(err)
	}
	for i, got := range queries {
		if got != "v9" {
			t.Errorf("request %d carried versionId=%q, want v9", i, got)
		}
	}
	if len(queries) != 3 {
		t.Errorf("requests = %d, want head/delete/get", len(queries))
	}
}

// The current object must not carry a versionId parameter at all: some stores
// reject an empty one.
func TestUnversionedObjectAddressingSendsNoParameter(t *testing.T) {
	var raw string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	})

	if _, err := c.HeadObject(context.Background(), "b", "k", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "versionId") {
		t.Errorf("query = %q, want no versionId", raw)
	}
}
