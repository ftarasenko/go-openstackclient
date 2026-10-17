//go:build koc_embedded

package watch

// s3Status finds nothing in a koc_embedded build, which has no S3 client.
func s3Status(error) (int, bool) { return 0, false }
