package s3

import (
	"crypto/md5" //nolint:gosec // G501: not a security choice — see contentMD5
	"encoding/base64"
)

// contentMD5 renders the Content-MD5 header S3 requires on the request bodies
// that carry a list of operations (DeleteObjects) and accepts on the small
// configuration PUTs (PutBucketVersioning).
//
// MD5 is not a security decision here and no alternative is available: the
// algorithm is fixed by the S3 protocol, the header exists so the server can
// reject a body corrupted in transit, and the request is already authenticated
// by a SHA-256 SigV4 signature over the same bytes. Omitting the header makes
// AWS refuse a batch delete outright.
func contentMD5(body []byte) string {
	sum := md5.Sum(body) //nolint:gosec // G401: protocol-mandated integrity check, not authentication
	return base64.StdEncoding.EncodeToString(sum[:])
}
