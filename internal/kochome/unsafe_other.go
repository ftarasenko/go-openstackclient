//go:build !unix

package kochome

import "os"

// unsafeDir accepts every directory: outside Unix, the temporary directory is
// already per-user, and the permission bits Go reports do not describe who may
// write to it.
func unsafeDir(os.FileInfo) string { return "" }
