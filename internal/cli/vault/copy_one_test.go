package vaultcli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// copyOne is the per-secret half of "vault kv copy". Driving it directly gets at
// every status and failure the result table can report without composing a full
// run's worth of fixtures for each one.
func TestCopySession_CopyOne(t *testing.T) {
	tests := []struct {
		name        string
		rel         string
		flags       copyFlags
		dstExists   bool
		dstWriteErr bool
		srcMissing  bool
		wantRow     []any
		wantSkipped bool
		wantWritten bool
		wantErr     string
	}{
		{
			name:        "a leaf copy writes and reports the key count",
			flags:       copyFlags{},
			wantRow:     []any{"src/dev", "dst/e2e", 1, statusCopied},
			wantWritten: true,
		},
		{
			name:        "a relative path is joined onto both ends",
			rel:         "openrc",
			wantRow:     []any{"src/dev/openrc", "dst/e2e/openrc", 1, statusCopied},
			wantWritten: true,
		},
		{
			// --dry-run still reads the source, so the reported key count is real
			// and read access is proven before anybody trusts the preview.
			name:    "dry run reads but does not write",
			flags:   copyFlags{dryRun: true},
			wantRow: []any{"src/dev", "dst/e2e", 1, statusWould},
		},
		{
			name:        "skip-existing reports zero keys and does not read the source",
			flags:       copyFlags{skipExisting: true},
			dstExists:   true,
			wantRow:     []any{"src/dev", "dst/e2e", 0, statusSkipped},
			wantSkipped: true,
		},
		{
			name:        "skip-existing copies when the destination is absent",
			flags:       copyFlags{skipExisting: true},
			wantRow:     []any{"src/dev", "dst/e2e", 1, statusCopied},
			wantWritten: true,
		},
		{
			// The key comes from the source Vault's own listing and is about to be
			// joined onto the destination path, so it can never be trusted.
			name:    "a traversing key is rejected before any request",
			rel:     "../../../prod/openrc",
			wantErr: `source "src/dev" returned an unsafe secret path`,
		},
		{
			name:       "an unreadable source fails the copy",
			srcMissing: true,
			wantErr:    `reading source "src/dev"`,
		},
		{
			name:        "a failed destination write fails the copy",
			dstWriteErr: true,
			wantErr:     `writing destination "dst/e2e"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wrote bool
			var srcReads []string
			srcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				srcReads = append(srcReads, r.URL.Path)
				if tt.srcMissing {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"errors":[]}`))
					return
				}
				_, _ = w.Write([]byte(`{"data":{"data":{"value":"x"}}}`))
			}))
			defer srcSrv.Close()

			dstSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost:
					if tt.dstWriteErr {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = w.Write([]byte(`{"errors":["sealed"]}`))
						return
					}
					wrote = true
					_, _ = w.Write([]byte(`{"data":{"version":1}}`))
				case tt.dstExists:
					_, _ = w.Write([]byte(`{"data":{"current_version":1}}`))
				default:
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"errors":[]}`))
				}
			}))
			defer dstSrv.Close()

			src, dst := clients(t, srcSrv.URL, dstSrv.URL)
			session := copySession{
				src: src, dst: dst,
				srcMount: src.KVMount(), dstMount: dst.KVMount(),
				opts: copyOptions{
					copyFlags:  tt.flags,
					srcPath:    "src/dev",
					dstPath:    "dst/e2e",
					srcDisplay: "src/dev",
				},
			}

			row, skipped, err := session.copyOne(context.Background(), tt.rel)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("copyOne() error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("copyOne() error = %v", err)
			}
			if !reflect.DeepEqual(row, tt.wantRow) {
				t.Errorf("copyOne() row = %v, want %v", row, tt.wantRow)
			}
			if skipped != tt.wantSkipped {
				t.Errorf("copyOne() skipped = %v, want %v", skipped, tt.wantSkipped)
			}
			if wrote != tt.wantWritten {
				t.Errorf("destination written = %v, want %v", wrote, tt.wantWritten)
			}
			if tt.wantSkipped && len(srcReads) != 0 {
				t.Errorf("source was read %v for a skipped secret; want no read at all", srcReads)
			}
		})
	}
}

// --src-version is forwarded as the version query parameter on the source read.
func TestCopySession_CopyOne_ForwardsSrcVersion(t *testing.T) {
	var gotQuery string
	srcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data":{"data":{"value":"x"}}}`))
	}))
	defer srcSrv.Close()
	dstSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"version":1}}`))
	}))
	defer dstSrv.Close()

	src, dst := clients(t, srcSrv.URL, dstSrv.URL)
	session := copySession{
		src: src, dst: dst,
		srcMount: src.KVMount(), dstMount: dst.KVMount(),
		opts: copyOptions{
			copyFlags:  copyFlags{srcVersion: 3},
			srcPath:    "src/dev",
			dstPath:    "dst/e2e",
			srcDisplay: "src/dev",
		},
	}
	if _, _, err := session.copyOne(context.Background(), ""); err != nil {
		t.Fatalf("copyOne() error = %v", err)
	}
	if gotQuery != "version=3" {
		t.Errorf("source read query = %q, want version=3", gotQuery)
	}
}
