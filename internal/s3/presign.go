package s3

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Presigned URLs. The credential moves out of the Authorization header and into
// the query string, which makes the whole request a URL anyone can fetch until
// it expires — the way to hand a colleague one backup without handing them a
// key. No request is made: this is pure local computation, so it works offline
// and leaves no trace on the store.
const (
	// MaxPresignExpiry is SigV4's ceiling on a presigned URL's lifetime.
	MaxPresignExpiry = 7 * 24 * time.Hour

	// unsignedPayload replaces the body hash: the signature cannot cover a body
	// the signer never sees, and for a GET there is none.
	unsignedPayload = "UNSIGNED-PAYLOAD"
)

// PresignGetObject returns a URL that fetches the object, valid for expiry.
// versionID selects a specific version, or "" for the current one.
//
// Anyone holding the URL can read the object until it expires, so treat one
// like a password: it is a bearer credential scoped to a single key.
func (c *Client) PresignGetObject(bucket, key, versionID string, expiry time.Duration) (string, error) {
	return c.presign(http.MethodGet, bucket, key, versionQuery(versionID), expiry)
}

// PresignPutObject returns a URL that uploads to the key, valid for expiry. The
// holder can write that one key and nothing else — which is how a machine with
// no S3 credentials at all delivers a dump.
func (c *Client) PresignPutObject(bucket, key string, expiry time.Duration) (string, error) {
	return c.presign(http.MethodPut, bucket, key, nil, expiry)
}

// presign builds a query-signed URL for one method/key pair.
func (c *Client) presign(method, bucket, key string, extra url.Values, expiry time.Duration) (string, error) {
	if c.cfg.Anonymous {
		return "", errors.New("cannot presign without credentials (--s3-anonymous is set)")
	}
	if key == "" {
		return "", errors.New("presigning needs an object key")
	}
	if expiry <= 0 {
		return "", fmt.Errorf("expiry must be positive, got %s", expiry)
	}
	if expiry > MaxPresignExpiry {
		return "", fmt.Errorf("expiry must be at most %s (SigV4's limit), got %s", MaxPresignExpiry, expiry)
	}

	utc := c.now().UTC()
	amzDate, dateStamp := utc.Format(amzDateFormat), utc.Format(dateStampFormat)
	scope := strings.Join([]string{dateStamp, c.cfg.Region, service, terminacc}, "/")

	q := url.Values{}
	for k, vs := range extra {
		q[k] = vs
	}
	q.Set("X-Amz-Algorithm", algorithm)
	q.Set("X-Amz-Credential", c.cfg.AccessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.Itoa(int(expiry.Seconds())))
	// Only host is signed: a browser or curl sends nothing else predictable,
	// and an unsigned header is one the holder cannot be forced to reproduce.
	q.Set("X-Amz-SignedHeaders", "host")

	u := c.url(bucket, key, q)

	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI(u),
		u.RawQuery, // already canonical: c.url renders it with canonicalQuery
		"host:" + u.Host + "\n",
		"host",
		unsignedPayload,
	}, "\n")

	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	sig := hmacSHA256(signingKey(c.cfg.SecretKey, dateStamp, c.cfg.Region), stringToSign)
	// Appended rather than added to q before signing: the signature is not part
	// of what it covers, and re-rendering the query would reorder the pairs.
	u.RawQuery += "&X-Amz-Signature=" + hexEncode(sig)
	return u.String(), nil
}
