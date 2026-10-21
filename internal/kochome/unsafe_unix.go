//go:build unix

package kochome

import (
	"os"
	"syscall"
)

// unsafeDir says why a directory another local user could control is unsafe to
// use as the embedded home, or "" when it is not.
func unsafeDir(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int64(st.Uid) != int64(os.Geteuid()) {
		return "belongs to another user"
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return "is writable by group or others"
	}
	return ""
}
