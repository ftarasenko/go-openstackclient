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
	"bytes"
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

	// MaxRetries is how many further attempts a retryable failure gets (0 =
	// none). See retry.go for what counts as retryable.
	MaxRetries int

	// Anonymous sends requests unsigned, for a bucket granted to everyone. It
	// is the only mode in which no credentials are required.
	Anonymous bool

	Debug bool
}

// Client is a minimal S3 REST client.
type Client struct {
	cfg  Config
	hc   *http.Client
	base *url.URL

	// now is the signing clock, overridden in tests.
	now func() time.Time
	// retryBase is the first backoff interval, overridden in tests so a retry
	// case does not spend real time asleep.
	retryBase time.Duration
}

// New validates the config and builds the client. It performs no network I/O:
// S3 has no login step, every request carries its own signature.
func New(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("S3 endpoint is required (--s3-endpoint / AWS_ENDPOINT_URL)")
	}
	if !cfg.Anonymous && (cfg.AccessKey == "" || cfg.SecretKey == "") {
		return nil, errors.New("S3 credentials are required (--s3-access-key/--s3-secret-key, AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY, or --s3-creds-from-ns); pass --s3-anonymous for a publicly readable bucket")
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
		cfg:       cfg,
		hc:        newHTTPClient(tlsCfg, cfg.Timeout),
		base:      base,
		now:       time.Now,
		retryBase: retryBaseDelay,
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

// ErrorCode returns the S3 error code carried by err ("BucketNotEmpty",
// "BucketAlreadyOwnedByYou"), or "" if err is not an S3 API error. Callers use
// it to turn a specific server answer into advice; matching on the message
// would break with the server's wording.
func ErrorCode(err error) string {
	var ae *APIError
	if !errors.As(err, &ae) {
		return ""
	}
	return ae.Code
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

	// IsPrefix marks a CommonPrefixes entry rather than a key: what a
	// delimited listing reports in place of everything below it. Only Key is
	// meaningful on one.
	IsPrefix bool

	// VersionID, IsLatest and DeleteMarker are filled only by a versioned
	// listing (ListOptions.Versions). A store without versioning — Garage has
	// none — never sets them.
	VersionID    string
	IsLatest     bool
	DeleteMarker bool
}

// ListOptions narrows a listing. The zero value lists every current object in
// the bucket.
type ListOptions struct {
	// Prefix restricts the listing to keys starting with it.
	Prefix string

	// Delimiter collapses everything after the next occurrence of it into a
	// single CommonPrefixes entry, which is how "/" turns a flat keyspace into
	// one directory level. Such entries arrive as Objects with IsPrefix set.
	Delimiter string

	// Limit caps the number of entries reported (0 = no cap). It is a hard
	// result cap, not merely a page size — the same rule the rest of koc's
	// --limit flags follow.
	Limit int

	// Versions lists every version and delete marker of each key instead of
	// only the current object, via the ?versions endpoint.
	Versions bool
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
	VersionID    string            // set only by a store with versioning on
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

// usEast1 is the one region whose name must NOT appear in a
// CreateBucketConfiguration: AWS treats it as the default and rejects the body
// that names it.
const usEast1 = "us-east-1"

// createBucketConfiguration is the CreateBucket request body. It exists because
// every region other than us-east-1 requires the bucket's location to be stated
// explicitly, and Garage validates the value against its configured region.
type createBucketConfiguration struct {
	XMLName            xml.Name `xml:"CreateBucketConfiguration"`
	LocationConstraint string   `xml:"LocationConstraint"`
}

// CreateBucket creates a bucket. The location constraint is the signing region,
// which is necessarily the right answer: a request signed for region R is only
// accepted by a store serving R.
//
// Creating a bucket the credentials already own is not an error on AWS outside
// us-east-1 either — it answers BucketAlreadyOwnedByYou, which the caller can
// recognise with ErrorCode.
func (c *Client) CreateBucket(ctx context.Context, bucket string) error {
	req := request{method: http.MethodPut, url: c.url(bucket, "", nil), payloadHash: emptySHA256}

	if region := c.cfg.Region; region != "" && region != usEast1 {
		body, err := xml.Marshal(createBucketConfiguration{LocationConstraint: region})
		if err != nil {
			return fmt.Errorf("encoding create-bucket body: %w", err)
		}
		req.body = bytes.NewReader(body)
		req.payloadHash = hexSHA256(body)
		req.size = int64(len(body))
		req.header = map[string]string{"Content-Type": "application/xml"}
	}

	return c.do(ctx, req, drainBody)
}

// DeleteBucket removes an empty bucket. A bucket with objects in it is refused
// by the server with BucketNotEmpty; nothing is deleted implicitly.
func (c *Client) DeleteBucket(ctx context.Context, bucket string) error {
	return c.do(ctx, request{method: http.MethodDelete, url: c.url(bucket, "", nil), payloadHash: emptySHA256}, drainBody)
}

// drainBody is the sink for a call whose reply carries nothing the caller wants.
// The body is still read so the connection goes back to the pool.
func drainBody(resp *http.Response) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, errorBodyLimit))
	return nil
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
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

// ListObjects lists objects in a bucket, following continuation tokens until the
// server says there are no more. limit caps the number of objects returned (0 =
// no cap) and is applied as a hard result cap, not merely as a page size — the
// same rule the rest of koc's --limit flags follow.
func (c *Client) ListObjects(ctx context.Context, bucket string, opts ListOptions) ([]Object, error) {
	var out []Object
	err := c.ListObjectsFunc(ctx, bucket, opts, func(o Object) error {
		out = append(out, o)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListObjectsFunc is ListObjects without materialising the result: fn is called
// once per key, in listing order, and an error from it stops the walk and is
// returned as-is. Callers that only fold over the listing (summing sizes,
// deleting as they go) use this so a bucket with a million keys costs one page
// of memory instead of all of them.
func (c *Client) ListObjectsFunc(ctx context.Context, bucket string, opts ListOptions, fn func(Object) error) error {
	if opts.Versions {
		return c.listObjectVersions(ctx, bucket, opts, fn)
	}

	token, seen := "", 0
	for {
		var page listObjectsPage
		u := c.url(bucket, "", listObjectsQuery(opts, token, seen))
		if err := c.getXML(ctx, u, &page); err != nil {
			return err
		}

		done, err := emitPage(&page, opts.Limit, &seen, fn)
		if err != nil || done {
			return err
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return nil
		}
		token = page.NextContinuationToken
	}
}

// listObjectsQuery renders one ListObjectsV2 request's parameters.
func listObjectsQuery(opts ListOptions, token string, seen int) url.Values {
	q := url.Values{"list-type": {"2"}, "max-keys": {strconv.Itoa(pageSize(opts.Limit, seen))}}
	if opts.Prefix != "" {
		q.Set("prefix", opts.Prefix)
	}
	if opts.Delimiter != "" {
		q.Set("delimiter", opts.Delimiter)
	}
	if token != "" {
		q.Set("continuation-token", token)
	}
	return q
}

// emitPage hands one page's entries to fn, counting them against limit. It
// reports done once the limit is reached, so the caller stops paging.
//
// CommonPrefixes come first so a delimited listing reads like a directory: the
// subtrees, then the keys at this level.
func emitPage(page *listObjectsPage, limit int, seen *int, fn func(Object) error) (done bool, err error) {
	for _, p := range page.CommonPrefixes {
		if err := fn(Object{Key: p.Prefix, IsPrefix: true}); err != nil {
			return false, err
		}
		if *seen++; limit > 0 && *seen >= limit {
			return true, nil
		}
	}
	for _, o := range page.Contents {
		err := fn(Object{
			Key:          o.Key,
			Size:         o.Size,
			LastModified: parseS3Time(o.LastModified),
			ETag:         strings.Trim(o.ETag, `"`),
			StorageClass: o.StorageClass,
			IsLatest:     true,
		})
		if err != nil {
			return false, err
		}
		if *seen++; limit > 0 && *seen >= limit {
			return true, nil
		}
	}
	return false, nil
}

// pageSize asks for a full page unless a limit means fewer keys are wanted.
func pageSize(limit, have int) int {
	if limit <= 0 || limit-have > maxKeysPerPage {
		return maxKeysPerPage
	}
	return limit - have
}

// versionQuery addresses one specific version of a key, or the current one when
// versionID is empty.
func versionQuery(versionID string) url.Values {
	if versionID == "" {
		return nil
	}
	return url.Values{"versionId": {versionID}}
}

// HeadBucket reports whether the bucket exists and the credentials may reach
// it, without listing anything. It is the cheapest possible probe: a bodiless
// request that costs the server no listing work.
func (c *Client) HeadBucket(ctx context.Context, bucket string) error {
	return c.do(ctx, request{method: http.MethodHead, url: c.url(bucket, "", nil), payloadHash: emptySHA256}, drainBody)
}

// HeadObject fetches an object's metadata without its body. versionID selects a
// specific version, or "" for the current one.
func (c *Client) HeadObject(ctx context.Context, bucket, key, versionID string) (*ObjectInfo, error) {
	var info *ObjectInfo
	err := c.do(ctx, request{method: http.MethodHead, url: c.url(bucket, key, versionQuery(versionID)), payloadHash: emptySHA256},
		func(resp *http.Response) error {
			info = objectInfoFromHeader(bucket, key, resp)
			info.VersionID = resp.Header.Get("x-amz-version-id")
			return nil
		})
	if err != nil {
		return nil, err
	}
	return info, nil
}

// GetObject streams an object's body to w and returns the number of bytes
// written.
func (c *Client) GetObject(ctx context.Context, bucket, key, versionID string, w io.Writer) (int64, error) {
	var n int64
	err := c.do(ctx, request{method: http.MethodGet, url: c.url(bucket, key, versionQuery(versionID)), payloadHash: emptySHA256},
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

// DeleteObject removes one object. S3 answers 204 for a key that was never
// there, so a delete is idempotent and "no such key" is not reported — the
// caller asked for the key to be gone, and it is.
func (c *Client) DeleteObject(ctx context.Context, bucket, key, versionID string) error {
	return c.do(ctx, request{method: http.MethodDelete, url: c.url(bucket, key, versionQuery(versionID)), payloadHash: emptySHA256}, drainBody)
}

// PutObject uploads body as a single request. The reader must be seekable
// because SigV4 signs a hash of the payload: the body is read once to hash it,
// then rewound and sent — which is also what lets a retry replay it.
//
// That rules out a pipe, and the server's single-part ceiling (5 GiB
// everywhere) applies. Callers that cannot promise either use
// PutObjectStream, which picks this path when the source is seekable and fits
// one part and goes multipart otherwise.
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
	put := request{
		method:      http.MethodPut,
		url:         c.url(bucket, key, nil),
		body:        body,
		payloadHash: hash,
		size:        size,
		header:      hdr,
	}
	err = c.do(ctx, put, func(resp *http.Response) error {
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
	return c.do(ctx, request{method: http.MethodGet, url: u, payloadHash: emptySHA256}, func(resp *http.Response) error {
		if err := xml.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding S3 response from %s: %w", u.Path, err)
		}
		return nil
	})
}

// request describes one signed S3 call. It is a struct rather than a parameter
// list because a bodyless GET/HEAD leaves most of it at its zero value, and
// four positional nils at every call site said nothing about which was which.
type request struct {
	method string
	url    *url.URL
	// body is seekable rather than a plain io.Reader so a retried attempt can
	// replay it — SigV4 already requires the payload to be readable twice, so
	// this costs nothing extra.
	body io.ReadSeeker // nil for GET/HEAD
	// payloadHash is the SigV4 hash of body; emptySHA256 when there is none.
	payloadHash string
	size        int64             // Content-Length; only read when body != nil
	header      map[string]string // extra request headers, e.g. Content-Type
}

// do performs a request, retrying a retryable failure up to cfg.MaxRetries
// times with exponential backoff. An error raised by sink is never retried —
// see nonRetryableError.
//
// Bodies are never logged, even under --debug: a request body is object data and
// a response body can be too, while the headers carry the signature. Method,
// path and status are enough to debug a 403.
func (c *Client) do(ctx context.Context, r request, sink func(*http.Response) error) error {
	start, err := bodyOffset(r.body)
	if err != nil {
		return err
	}

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if err := c.backoff(ctx, attempt); err != nil {
				return err
			}
			if r.body != nil {
				if _, err := r.body.Seek(start, io.SeekStart); err != nil {
					return fmt.Errorf("rewinding request body for retry: %w", err)
				}
			}
		}

		err := c.attempt(ctx, r, sink)
		if err == nil {
			return nil
		}
		if attempt >= c.cfg.MaxRetries || ctx.Err() != nil || !retryable(err) {
			return unwrapSink(err)
		}
		if c.cfg.Debug {
			fmt.Fprintf(os.Stderr, "s3: %s %s failed (%v); retrying\n", r.method, r.url.RequestURI(), err)
		}
	}
}

// bodyOffset records where a replayable body starts, so a retry rewinds to the
// caller's position rather than to byte zero of, say, an open file.
func bodyOffset(body io.ReadSeeker) (int64, error) {
	if body == nil {
		return 0, nil
	}
	at, err := body.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, fmt.Errorf("seeking request body: %w", err)
	}
	return at, nil
}

// unwrapSink strips the marker retryable() keys off, so callers see the error
// the sink actually returned.
func unwrapSink(err error) error {
	var sink *nonRetryableError
	if errors.As(err, &sink) {
		return sink.err
	}
	return err
}

// attempt performs one signed request, then hands the still-open response to
// sink. Owning the response lifecycle here (rather than returning it) keeps
// every body closed on every path, including the error ones.
func (c *Client) attempt(ctx context.Context, r request, sink func(*http.Response) error) error {
	// A nil io.ReadSeeker in an interface-typed argument is not a nil
	// io.Reader, and net/http treats the difference as "body of unknown
	// length" — so the nil case has to be passed explicitly.
	var rc io.Reader
	if r.body != nil {
		rc = r.body
	}
	req, err := http.NewRequestWithContext(ctx, r.method, r.url.String(), rc)
	if err != nil {
		return err
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	if r.body != nil {
		req.ContentLength = r.size
	}
	if !c.cfg.Anonymous {
		c.sign(req, r.payloadHash, c.now())
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("s3 %s %s: %w", r.method, r.url.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if c.cfg.Debug {
		fmt.Fprintf(os.Stderr, "s3: %s %s -> %d\n", r.method, r.url.RequestURI(), resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newAPIError(r.method, r.url.Path, resp)
	}
	if err := sink(resp); err != nil {
		return &nonRetryableError{err: err}
	}
	return nil
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
