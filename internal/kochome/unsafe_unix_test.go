//go:build unix

package kochome

import (
	"io/fs"
	"os"
	"syscall"
	"testing"
	"time"
)

// dirInfo is a directory's FileInfo with the owner and mode a test chooses.
type dirInfo struct {
	mode fs.FileMode
	uid  uint32
}

func (d dirInfo) Name() string       { return "koc-home" }
func (d dirInfo) Size() int64        { return 0 }
func (d dirInfo) Mode() fs.FileMode  { return fs.ModeDir | d.mode }
func (d dirInfo) ModTime() time.Time { return time.Time{} }
func (d dirInfo) IsDir() bool        { return true }
func (d dirInfo) Sys() any           { return &syscall.Stat_t{Uid: d.uid} }

func TestUnsafeDir(t *testing.T) {
	me := uint32(os.Geteuid())
	for _, tc := range []struct {
		name string
		fi   dirInfo
		want string
	}{
		{"mine, 0700", dirInfo{0o700, me}, ""},
		{"mine, 0755", dirInfo{0o755, me}, ""},
		{"another user's", dirInfo{0o700, me + 1}, "belongs to another user"},
		{"mine, group-writable", dirInfo{0o770, me}, "is writable by group or others"},
		{"mine, world-writable", dirInfo{0o707, me}, "is writable by group or others"},
	} {
		if got := unsafeDir(tc.fi); got != tc.want {
			t.Errorf("%s: unsafeDir = %q, want %q", tc.name, got, tc.want)
		}
	}
}
