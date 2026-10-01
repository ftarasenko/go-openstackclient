package s3cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// An unknown -c fails every write verb before the store sees a request: once
// it has one, an error reads as a failed write and a retry repeats it.
func TestWriteVerbs_UnknownColumnSendsNothing(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	verbs := map[string]func(*s3.Client, *output.Options) error{
		"bucket create": func(c *s3.Client, o *output.Options) error {
			return runBucketCreate(ctx, c, o, "b", io.Discard)
		},
		"bucket set": func(c *s3.Client, o *output.Options) error {
			return runBucketSet(ctx, c, o, "b", "Enabled", io.Discard)
		},
		"upload file": func(c *s3.Client, o *output.Options) error {
			return runUpload(ctx, c, o, uploadRequest{src: file, bucket: "b", flags: &uploadFlags{}}, io.Discard)
		},
		"upload stdin": func(c *s3.Client, o *output.Options) error {
			return runUpload(ctx, c, o, uploadRequest{src: "-", bucket: "b", key: "k", flags: &uploadFlags{}}, io.Discard)
		},
		"copy": func(c *s3.Client, o *output.Options) error {
			return runCopy(ctx, c, o, s3.ObjectRef{Bucket: "b", Key: "a"}, s3.ObjectRef{Bucket: "b", Key: "c"}, &copyFlags{}, io.Discard)
		},
	}
	for name, run := range verbs {
		t.Run(name, func(t *testing.T) {
			var hits int
			c := newMockClient(t, func(http.ResponseWriter, *http.Request) { hits++ })
			err := run(c, &output.Options{Format: output.FormatValue, Columns: []string{"bogus"}})
			var ce *output.ColumnError
			if !errors.As(err, &ce) || ce.Rendering {
				t.Fatalf("err = %v, want a pre-flight ColumnError", err)
			}
			if hits != 0 {
				t.Errorf("%d request(s) sent despite the bad column", hits)
			}
		})
	}
}
