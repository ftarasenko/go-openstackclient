// Package s3 is a dependency-free, minimal S3 client. It exists so koc can list
// buckets and objects and move files in and out of an S3-compatible store —
// KeyStack's LCM cluster ships Garage, which holds the GitLab object storage and
// the scheduled MariaDB backups — without vendoring aws-sdk-go-v2 or minio-go.
// Both would add a dozen modules to the offline vendor tree and megabytes to a
// single-binary product, for four HTTP calls and one signing algorithm the
// standard library already has the primitives for. The same trade-off was made
// for internal/vault and internal/kube.
//
// Only what koc needs is implemented: ListBuckets, ListObjectsV2, HeadObject,
// GetObject and a single-part PutObject. There is no multipart upload, no
// bucket/key administration and no presigning.
package s3

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultRegion is the region KeyStack's Garage deployment reports, and the one
// its S3 error bodies echo back. Garage ignores the value beyond checking that
// the signature was computed over it, but it must match, so a default that fits
// the target cloud saves every invocation a flag.
const DefaultRegion = "garage"

// maxKeysPerPage is the page size for ListObjectsV2. 1000 is the protocol
// maximum and what every server defaults to.
const maxKeysPerPage = 1000

// errorBodyLimit caps how much of a non-2xx body is read before parsing. S3
// errors are small XML documents; a huge body means something other than S3 is
// answering, and reading it all would be the bug.
const errorBodyLimit = 64 << 10

// Config holds S3 connection and credential settings.
type Config struct {
	// Endpoint is the S3 API base URL. A bare host ("s3.example.com") is taken
	// as https.
	Endpoint string
	Region   string // signing region; empty → DefaultRegion

	AccessKey string
	SecretKey string

	// PathStyle addresses a bucket as <endpoint>/<bucket>/<key> rather than
	// <bucket>.<endpoint>/<key>. Garage behind a single-hostname gateway (and
	// any deployment without a wildcard DNS record) requires it, which is why
	// the CLI defaults it on.
	PathStyle bool

	CACertPEM []byte // optional CA bundle for the endpoint's TLS
	Insecure  bool   // skip TLS verification

	// Timeout caps a whole request. Unlike internal/vault this has no default:
	// zero means unbounded, because an object transfer has no sensible fixed
	// cap. A wedged endpoint is still caught by responseHeaderTimeout.
	Timeout time.Duration

	Debug bool
}

// Client is a minimal S3 REST client.
type Client struct {
	cfg  Config
	hc   *http.Client
	base *url.URL

	// now is the signing clock, overridden in tests.
	now func() time.Time
}

// New validates the config and builds the client. It performs no network I/O:
// S3 has no login step, every request carries its own signature.
func New(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("S3 endpoint is required (--s3-endpoint / AWS_ENDPOINT_URL)")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("S3 credentials are required (--s3-access-key/--s3-secret-key, AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY, or --s3-creds-from-ns)")
	}
	if cfg.Region == "" {
		cfg.Region = DefaultRegion
	}

	base, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case cfg.Insecure:
		tlsCfg.InsecureSkipVerify = true
		warnInsecure("the S3 endpoint at " + base.String() + " (--insecure-s3)")
	case len(cfg.CACertPEM) > 0:
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CACertPEM) {
			return nil, errors.New("no certificates parsed from S3 CA bundle")
		}
		tlsCfg.RootCAs = pool
	}
	if base.Scheme == "http" {
		warnCleartext(base.String(), base.Hostname())
	}

	return &Client{
		cfg:  cfg,
		hc:   newHTTPClient(tlsCfg, cfg.Timeout),
		base: base,
		now:  time.Now,
	}, nil
}

// parseEndpoint accepts a full URL or a bare host, defaulting to https, and
// strips any path so a trailing slash or a copied console URL cannot end up
// prefixed to every key.
func parseEndpoint(endpoint string) (*url.URL, error) {
	raw := endpoint
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid S3 endpoint %q: %w", endpoint, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid S3 endpoint %q: no host", endpoint)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("invalid S3 endpoint %q: scheme must be http or https", endpoint)
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

// Endpoint returns the base URL the client talks to, so a caller can report
// where it read from or wrote to.
func (c *Client) Endpoint() string { return c.base.String() }

// Region returns the signing region.
func (c *Client) Region() string { return c.cfg.Region }

// APIError is a non-2xx S3 response, with the fields S3 puts in its XML error
// body. Code is the stable, machine-readable part ("NoSuchKey",
// "AccessDenied"), so callers match on it rather than on the message.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Resource   string
	RequestID  string
	Method     string
	Path       string
}

func (e *APIError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("s3 %s %s: %s (%s): %s", e.Method, e.Path, http.StatusText(e.StatusCode), e.Code, e.Message)
	case e.Code != "":
		return fmt.Sprintf("s3 %s %s: %s (%s)", e.Method, e.Path, http.StatusText(e.StatusCode), e.Code)
	default:
		return fmt.Sprintf("s3 %s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	}
}

// IsNotFound reports whether err is an S3 "it isn't there" answer. HEAD returns
// a bodiless 404, so the status has to count as much as the code.
func IsNotFound(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.Code {
	case "NoSuchKey", "NoSuchBucket":
		return true
	}
	return ae.StatusCode == http.StatusNotFound
}

// Bucket is one entry of a ListBuckets result.
type Bucket struct {
	Name         string
	CreationDate time.Time
}

// Object is one entry of a ListObjects result.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
	StorageClass string
}

// ObjectInfo describes a single object, as returned by HeadObject (and by
// PutObject for what it just wrote).
type ObjectInfo struct {
	Bucket       string
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
	ContentType  string
	Metadata     map[string]string // x-amz-meta-*, with the prefix stripped
}

// ListBuckets returns the buckets the credentials can see. Note that on Garage
// this is scoped to the access key: a key granted one bucket lists exactly that
// bucket, not the cluster's.
func (c *Client) ListBuckets(ctx context.Context) ([]Bucket, error) {
	var result struct {
		Buckets struct {
			Bucket []struct {
				Name         string `xml:"Name"`
				CreationDate string `xml:"CreationDate"`
			} `xml:"Bucket"`
		} `xml:"Buckets"`
	}
	if err := c.getXML(ctx, c.url("", "", nil), &result); err != nil {
		return nil, err
	}

	out := make([]Bucket, 0, len(result.Buckets.Bucket))
	for _, b := range result.Buckets.Bucket {
		out = append(out, Bucket{Name: b.Name, CreationDate: parseS3Time(b.CreationDate)})
	}
	return out, nil
}

// listObjectsPage is one ListObjectsV2 response.
type listObjectsPage struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		StorageClass string `xml:"StorageClass"`
	} `xml:"Contents"`
}

// ListObjects lists objects in a bucket, following continuation tokens until the
// server says there are no more. limit caps the number of objects returned (0 =
// no cap) and is applied as a hard result cap, not merely as a page size — the
// same rule the rest of koc's --limit flags follow.
func (c *Client) ListObjects(ctx context.Context, bucket, prefix string, limit int) ([]Object, error) {
	var out []Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "max-keys": {strconv.Itoa(pageSize(limit, len(out)))}}
		if prefix != "" {
			q.Set("prefix", prefix)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}

		var page listObjectsPage
		if err := c.getXML(ctx, c.url(bucket, "", q), &page); err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			out = append(out, Object{
				Key:          o.Key,
				Size:         o.Size,
				LastModified: parseS3Time(o.LastModified),
				ETag:         strings.Trim(o.ETag, `"`),
				StorageClass: o.StorageClass,
			})
			if limit > 0 && len(out) >= limit {
				return out, nil
			}
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}

// pageSize asks for a full page unless a limit means fewer keys are wanted.
func pageSize(limit, have int) int {
	if limit <= 0 || limit-have > maxKeysPerPage {
		return maxKeysPerPage
	}
	return limit - have
}

// HeadObject fetches an object's metadata without its body.
func (c *Client) HeadObject(ctx context.Context, bucket, key string) (*ObjectInfo, error) {
	var info *ObjectInfo
	err := c.do(ctx, http.MethodHead, c.url(bucket, key, nil), nil, emptySHA256, 0, nil,
		func(resp *http.Response) error {
			info = objectInfoFromHeader(bucket, key, resp)
			return nil
		})
	if err != nil {
		return nil, err
	}
	return info, nil
}

// GetObject streams an object's body to w and returns the number of bytes
// written.
func (c *Client) GetObject(ctx context.Context, bucket, key string, w io.Writer) (int64, error) {
	var n int64
	err := c.do(ctx, http.MethodGet, c.url(bucket, key, nil), nil, emptySHA256, 0, nil,
		func(resp *http.Response) error {
			var cerr error
			n, cerr = io.Copy(w, resp.Body)
			if cerr != nil {
				return fmt.Errorf("reading %s/%s: %w", bucket, key, cerr)
			}
			return nil
		})
	return n, err
}

// PutObject uploads body as a single part. The reader must be seekable because
// SigV4 signs a hash of the payload: the body is read once to hash it, then
// rewound and sent. That rules out streaming from a pipe, and is the reason
// there is no multipart support here — for the backup-sized objects koc moves,
// a single signed PUT is enough, and an S3 server's own single-part ceiling
// (5 GiB) applies.
func (c *Client) PutObject(ctx context.Context, bucket, key string, body io.ReadSeeker, size int64, contentType string) (*ObjectInfo, error) {
	hash, err := hashSeeker(body)
	if err != nil {
		return nil, err
	}
	hdr := map[string]string{}
	if contentType != "" {
		hdr["Content-Type"] = contentType
	}

	var info *ObjectInfo
	err = c.do(ctx, http.MethodPut, c.url(bucket, key, nil), body, hash, size, hdr,
		func(resp *http.Response) error {
			// Drain so the connection can be reused; a PUT reply has no body worth
			// keeping beyond the ETag in its headers.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, errorBodyLimit))
			info = &ObjectInfo{
				Bucket:      bucket,
				Key:         key,
				Size:        size,
				ETag:        strings.Trim(resp.Header.Get("ETag"), `"`),
				ContentType: contentType,
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return info, nil
}

// hashSeeker computes the SHA-256 of everything from the reader's current
// position to EOF, then rewinds to where it started.
func hashSeeker(r io.ReadSeeker) (string, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return "", fmt.Errorf("seeking upload source: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("hashing upload source: %w", err)
	}
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewinding upload source: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// objectInfoFromHeader reads the object metadata S3 returns in response headers.
func objectInfoFromHeader(bucket, key string, resp *http.Response) *ObjectInfo {
	info := &ObjectInfo{
		Bucket:      bucket,
		Key:         key,
		Size:        resp.ContentLength,
		ETag:        strings.Trim(resp.Header.Get("ETag"), `"`),
		ContentType: resp.Header.Get("Content-Type"),
	}
	if v := resp.Header.Get("Content-Length"); v != "" && info.Size < 0 {
		// A HEAD response has no body, so Go may report ContentLength as -1
		// while the header itself is present and authoritative.
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			info.Size = n
		}
	}
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		info.LastModified = t
	}
	for name, vals := range resp.Header {
		const prefix = "X-Amz-Meta-"
		if len(vals) == 0 || !strings.HasPrefix(name, prefix) {
			continue
		}
		if info.Metadata == nil {
			info.Metadata = map[string]string{}
		}
		info.Metadata[strings.ToLower(strings.TrimPrefix(name, prefix))] = vals[0]
	}
	return info
}

// url builds a request URL for a bucket/key pair, in path or virtual-host style.
// RawPath is set to the SigV4 encoding of the path so the signed canonical URI
// and the request line are the same bytes (see canonicalURI).
func (c *Client) url(bucket, key string, q url.Values) *url.URL {
	u := *c.base
	path := "/"
	switch {
	case bucket == "":
	case c.cfg.PathStyle:
		path = "/" + bucket
		if key != "" {
			path += "/" + strings.TrimPrefix(key, "/")
		}
	default:
		u.Host = bucket + "." + u.Host
		if key != "" {
			path = "/" + strings.TrimPrefix(key, "/")
		}
	}
	u.Path = path
	u.RawPath = uriEncode(path, true)
	if len(q) > 0 {
		u.RawQuery = canonicalQuery(q)
	}
	return &u
}

// getXML performs a signed GET and decodes the XML body into out.
func (c *Client) getXML(ctx context.Context, u *url.URL, out any) error {
	return c.do(ctx, http.MethodGet, u, nil, emptySHA256, 0, nil, func(resp *http.Response) error {
		if err := xml.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding S3 response from %s: %w", u.Path, err)
		}
		return nil
	})
}

// do signs and performs one request, then hands the still-open response to sink.
// Owning the response lifecycle here (rather than returning it) keeps every body
// closed on every path, including the error ones.
//
// Bodies are never logged, even under --debug: a request body is object data and
// a response body can be too, while the headers carry the signature. Method,
// path and status are enough to debug a 403.
func (c *Client) do(ctx context.Context, method string, u *url.URL, body io.Reader, payloadHash string,
	size int64, hdr map[string]string, sink func(*http.Response) error) error {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.ContentLength = size
	}
	c.sign(req, payloadHash, c.now())

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("s3 %s %s: %w", method, u.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if c.cfg.Debug {
		fmt.Fprintf(os.Stderr, "s3: %s %s -> %d\n", method, u.RequestURI(), resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newAPIError(method, u.Path, resp)
	}
	return sink(resp)
}

// newAPIError builds an APIError from a failed response, parsing S3's XML error
// document when there is one. HEAD replies have no body at all, which is why the
// status code is carried separately.
func newAPIError(method, path string, resp *http.Response) error {
	e := &APIError{
		StatusCode: resp.StatusCode,
		Method:     method,
		Path:       path,
		RequestID:  resp.Header.Get("X-Amz-Request-Id"),
	}
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))

	var doc struct {
		Code      string `xml:"Code"`
		Message   string `xml:"Message"`
		Resource  string `xml:"Resource"`
		RequestID string `xml:"RequestId"`
	}
	if xml.Unmarshal(payload, &doc) == nil {
		e.Code, e.Message, e.Resource = doc.Code, doc.Message, doc.Resource
		if doc.RequestID != "" {
			e.RequestID = doc.RequestID
		}
	}
	if e.Code == "" && resp.StatusCode == http.StatusNotFound {
		e.Code = "NoSuchKey"
	}
	return e
}

// parseS3Time parses the timestamps S3 puts in list results. They are ISO 8601 /
// RFC 3339, with or without fractional seconds; an unparseable value yields the
// zero time rather than failing the whole listing, since a timestamp is never
// the reason a caller asked.
func parseS3Time(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
