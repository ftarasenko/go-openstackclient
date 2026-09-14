package s3cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// --delimiter turns a flat keyspace into one directory level: the subtrees come
// first, with no size, so a table never claims an empty object is there.
func TestRunObjectListDelimiter(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("delimiter"); got != "/" {
			t.Errorf("delimiter = %q, want /", got)
		}
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<CommonPrefixes><Prefix>2026/</Prefix></CommonPrefixes>
			<Contents><Key>latest.sql.gz</Key><Size>7</Size>
				<LastModified>2026-09-14T10:00:00.000Z</LastModified><ETag>"e"</ETag></Contents>
			</ListBucketResult>`)
	})

	var buf bytes.Buffer
	err := runObjectList(context.Background(), client, valueOpts(), "db-backups",
		&objectListFlags{delimiter: "/"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	want := "2026/\t\t\t\nlatest.sql.gz\t7\t2026-09-14T10:00:00Z\te\n"
	if got := buf.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestRunObjectListHuman(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>big.sql.gz</Key><Size>15252880</Size></Contents>
			</ListBucketResult>`)
	})

	var buf bytes.Buffer
	err := runObjectList(context.Background(), client, valueOpts(), "db-backups",
		&objectListFlags{human: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "14.5 MiB") {
		t.Errorf("output = %q, want a human-readable size", buf.String())
	}
}

// A local filter has to be applied before --limit is counted, or a bucket whose
// first keys are all excluded would answer an empty list under "--limit 1".
func TestRunObjectListFilterThenLimit(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		// With filtering on, the cap moves to koc, so the server must not be
		// asked for a short page.
		if got := r.URL.Query().Get("max-keys"); got != "1000" {
			t.Errorf("max-keys = %q, want a full page when filtering locally", got)
		}
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>e2e-a.gz</Key><Size>1</Size></Contents>
			<Contents><Key>nightly-a.gz</Key><Size>2</Size></Contents>
			<Contents><Key>nightly-b.gz</Key><Size>3</Size></Contents>
			</ListBucketResult>`)
	})

	f := &objectListFlags{limit: 1, filter: keyFilter{include: []string{"nightly-*"}}}
	if err := f.filter.compile(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := runObjectList(context.Background(), client, valueOpts(), "db-backups", f, &buf); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "nightly-a.gz\t2\t\t\n"; got != want {
		t.Errorf("output = %q, want the first matching key only", got)
	}
}

// --all-versions adds two columns, so the extra fields are not silently dropped
// in --format value.
func TestRunObjectListAllVersions(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.Query().Has("versions") {
			t.Error("the request did not carry ?versions")
		}
		_, _ = fmt.Fprint(w, `<ListVersionsResult><IsTruncated>false</IsTruncated>
			<Version><Key>dump.gz</Key><VersionId>v2</VersionId><IsLatest>true</IsLatest>
				<Size>9</Size><LastModified>2026-09-14T10:00:00.000Z</LastModified></Version>
			<DeleteMarker><Key>gone.gz</Key><VersionId>v1</VersionId>
				<LastModified>2026-09-14T09:00:00.000Z</LastModified></DeleteMarker>
			</ListVersionsResult>`)
	})

	var buf bytes.Buffer
	err := runObjectList(context.Background(), client, valueOpts(), "b",
		&objectListFlags{allVersions: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "v2") || !strings.Contains(out, "true") {
		t.Errorf("output %q does not carry the version columns", out)
	}
	if !strings.Contains(out, "v1 (delete marker)") {
		t.Errorf("output %q does not mark the delete marker", out)
	}
}

func TestRunDuHuman(t *testing.T) {
	client := newMockClient(t, duListHandler(t))

	var buf bytes.Buffer
	err := runDu(context.Background(), client, valueOpts(), "db-backups", "", &duFlags{human: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "db-backups\n\n3\n123 B\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestRunDuFilter(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>a.sql.gz</Key><Size>100</Size></Contents>
			<Contents><Key>a.sha256</Key><Size>7</Size></Contents>
			</ListBucketResult>`)
	})

	f := &duFlags{filter: keyFilter{include: []string{"*.sql.gz"}}}
	if err := f.filter.compile(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := runDu(context.Background(), client, valueOpts(), "db-backups", "", f, &buf); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "db-backups\n\n1\n100\n"; got != want {
		t.Errorf("output = %q, want only the filtered object counted", got)
	}
}

func TestRunBucketShow(t *testing.T) {
	var methods []string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.URL.Query().Has("versioning") {
			_, _ = fmt.Fprint(w, `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	var buf bytes.Buffer
	if err := runBucketShow(context.Background(), client, valueOpts(), "db-backups", &buf); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(methods, ","), "HEAD,GET"; got != want {
		t.Errorf("requests = %q, want a HEAD then the versioning read", got)
	}
	out := buf.String()
	if !strings.Contains(out, "db-backups") || !strings.Contains(out, "Enabled") {
		t.Errorf("output = %q", out)
	}
}

// The bucket being there is the answer the command was asked for; a store that
// does not implement the versioning call must not turn that into a failure.
func TestRunBucketShowToleratesNoVersioningSupport(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("versioning") {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	var buf bytes.Buffer
	if err := runBucketShow(context.Background(), client, valueOpts(), "db-backups", &buf); err != nil {
		t.Fatalf("a store without versioning failed the command: %v", err)
	}
	if !strings.Contains(buf.String(), "unknown") {
		t.Errorf("output = %q, want the versioning field unknown", buf.String())
	}
}

func TestRunBucketShowMissingBucket(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	var buf bytes.Buffer
	err := runBucketShow(context.Background(), client, valueOpts(), "absent", &buf)
	if err == nil {
		t.Fatal("a missing bucket was reported as present")
	}
	if !strings.Contains(err.Error(), "absent") {
		t.Errorf("error %q does not name the bucket", err)
	}
}

func TestRunBucketSet(t *testing.T) {
	var gotMethod string
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	})

	var buf bytes.Buffer
	if err := runBucketSet(context.Background(), client, valueOpts(), "b", "Enabled", &buf); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if got, want := buf.String(), "b\nEnabled\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// The flag takes either spelling, so "--versioning Enabled" is not a puzzling
// error — and anything else is refused by name.
func TestVersioningStatus(t *testing.T) {
	for _, in := range []string{"enabled", "Enabled", "ENABLED"} {
		if got, err := versioningStatus(in); err != nil || got != "Enabled" {
			t.Errorf("versioningStatus(%q) = %q, %v", in, got, err)
		}
	}
	if got, err := versioningStatus("suspended"); err != nil || got != "Suspended" {
		t.Errorf("versioningStatus(suspended) = %q, %v", got, err)
	}
	if _, err := versioningStatus("unversioned"); err == nil {
		t.Error("unversioned was accepted as a settable state")
	}
}

// Presigning makes no request at all, which is why runPresign takes no context.
func TestRunPresign(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("presigning sent %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})

	var buf bytes.Buffer
	err := runPresign(client, valueOpts(), "db-backups", "dump.sql.gz", "", "get", time.Hour, &buf)
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"X-Amz-Signature=", "X-Amz-Expires=3600", "/db-backups/dump.sql.gz"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestRunPresignPut(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	var buf bytes.Buffer
	if err := runPresign(client, valueOpts(), "b", "k", "", "put", time.Minute, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "put") {
		t.Errorf("output %q does not name the method", buf.String())
	}
}

func TestRunPresignRejectsBadInput(t *testing.T) {
	client := newMockClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	var buf bytes.Buffer
	if err := runPresign(client, valueOpts(), "b", "k", "", "delete", time.Hour, &buf); err == nil ||
		!strings.Contains(err.Error(), "get or put") {
		t.Errorf("err = %v, want it to name the allowed methods", err)
	}
	// A version ID addresses an object that exists; presigning an upload to one
	// is a contradiction.
	if err := runPresign(client, valueOpts(), "b", "k", "v1", "put", time.Hour, &buf); err == nil {
		t.Error("--version-id with --method put was accepted")
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{15252880, "14.5 MiB"},
		{1 << 30, "1.00 GiB"},
		{200 << 30, "200 GiB"},
		{-2048, "-2.00 KiB"},
	} {
		if got := output.HumanBytes(tc.n); got != tc.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
