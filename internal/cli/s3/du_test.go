package s3cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestRunDu(t *testing.T) {
	client := newMockClient(t, duListHandler(t))

	var buf bytes.Buffer
	err := runDu(context.Background(), client, valueOpts(), "db-backups", "", &duFlags{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	// Bucket, prefix, objects, exact bytes — 100 + 20 + 3, one field per line.
	if got, want := buf.String(), "db-backups\n\n3\n123\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestRunDuGroupByStorageClass(t *testing.T) {
	client := newMockClient(t, duListHandler(t))

	var buf bytes.Buffer
	err := runDu(context.Background(), client, valueOpts(), "db-backups", "", &duFlags{group: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	// Classes sort by name, and an object with no class counts as STANDARD.
	want := "GLACIER\t1\t20\nSTANDARD\t2\t103\n"
	if got := buf.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// duListHandler serves one page of three objects across two storage classes,
// one of which the store reports with no class at all.
func duListHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("du issued %s, want GET only", r.Method)
		}
		_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>
			<Contents><Key>a</Key><Size>100</Size><StorageClass>STANDARD</StorageClass></Contents>
			<Contents><Key>b</Key><Size>20</Size><StorageClass>GLACIER</StorageClass></Contents>
			<Contents><Key>c</Key><Size>3</Size></Contents>
			</ListBucketResult>`)
	}
}

// du must be able to render as a table too, not only in --format value.
func TestRunDuTable(t *testing.T) {
	client := newMockClient(t, duListHandler(t))

	var buf bytes.Buffer
	o := &output.Options{Format: output.FormatJSON}
	if err := runDu(context.Background(), client, o, "db-backups", "e2e-", &duFlags{}, &buf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"Objects": 3`, `"Size": 123`, `"Prefix": "e2e-"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("JSON output %s missing %q", buf.String(), want)
		}
	}
}
