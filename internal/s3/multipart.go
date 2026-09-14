package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Multipart upload. Two things force it, and PutObject can do neither:
//
//   - the single-part ceiling. A signed PUT carries the whole object in one
//     request body, and every S3 implementation caps that at 5 GiB, so a
//     database dump past that size simply could not be uploaded.
//   - an unseekable source. SigV4 signs a hash of the payload, so PutObject has
//     to read its body twice and therefore demands an io.ReadSeeker. A pipe
//     ("mysqldump | koc s3 upload -") cannot provide one. Multipart hashes each
//     part separately, so the source only ever has to be read forwards.
const (
	// MinPartSize is the floor S3 puts on every part but the last.
	MinPartSize = 5 << 20

	// DefaultPartSize is the part size koc uses when none is given. Memory cost
	// is PartSize × Concurrency, so this pairs with DefaultConcurrency for a
	// 64 MiB working set — a deliberate choice for the air-gapped cluster nodes
	// koc runs on, where s5cmd's 50 MiB × 5 would be a quarter of a gigabyte.
	DefaultPartSize = 16 << 20

	// DefaultConcurrency is how many parts are uploaded at once by default.
	DefaultConcurrency = 4

	// maxParts is the protocol's limit on parts per upload. With the default
	// part size that caps one object at 160 GiB; --part-size raises it.
	maxParts = 10000
)

// UploadOptions configures PutObjectStream.
type UploadOptions struct {
	ContentType string

	// PartSize is the size of each part but the last (0 = DefaultPartSize).
	// Values below MinPartSize are rejected rather than silently raised, so a
	// "--part-size 1" does not quietly become something else.
	PartSize int64

	// Concurrency is how many parts travel at once (0 = DefaultConcurrency).
	Concurrency int

	// Size is the source's length when it is known, or -1 when it is not (a
	// pipe). It only selects the strategy: a known size at or below PartSize
	// goes out as a single PUT, which is one request instead of three.
	Size int64
}

// withDefaults validates the options and fills the zero values in.
func (o UploadOptions) withDefaults() (UploadOptions, error) {
	if o.PartSize == 0 {
		o.PartSize = DefaultPartSize
	}
	if o.PartSize < MinPartSize {
		return o, fmt.Errorf("part size must be at least %d bytes (S3's minimum), got %d", MinPartSize, o.PartSize)
	}
	if o.Concurrency == 0 {
		o.Concurrency = DefaultConcurrency
	}
	if o.Concurrency < 1 {
		return o, fmt.Errorf("concurrency must be at least 1, got %d", o.Concurrency)
	}
	if o.Size == 0 {
		// A zero-length object is legitimate and must not be mistaken for
		// "unknown"; callers say unknown with a negative value.
		o.Size = 0
	}
	return o, nil
}

// PutObjectStream uploads from r, choosing the single-PUT or multipart path by
// what opts.Size says about the source.
//
// A multipart upload that fails is aborted, so a half-written object never
// becomes visible and the parts already stored stop accruing cost. If the abort
// itself fails the returned error says so — the upload ID is then the operator's
// to clean up.
func (c *Client) PutObjectStream(ctx context.Context, bucket, key string, r io.Reader,
	opts UploadOptions) (*ObjectInfo, error) {
	opts, err := opts.withDefaults()
	if err != nil {
		return nil, err
	}

	// A source whose length is known and fits one part is cheaper as a plain
	// PUT, but only if it is seekable — SigV4 has to hash it before sending.
	if seeker, ok := r.(io.ReadSeeker); ok && opts.Size >= 0 && opts.Size <= opts.PartSize {
		return c.PutObject(ctx, bucket, key, seeker, opts.Size, opts.ContentType)
	}

	uploadID, err := c.createMultipartUpload(ctx, bucket, key, opts.ContentType)
	if err != nil {
		return nil, err
	}

	parts, err := c.uploadParts(ctx, bucket, key, uploadID, r, opts)
	if err != nil {
		if abortErr := c.abortMultipartUpload(ctx, bucket, key, uploadID); abortErr != nil {
			return nil, fmt.Errorf("%w (and aborting upload %s failed: %w)", err, uploadID, abortErr)
		}
		return nil, err
	}
	return c.completeMultipartUpload(ctx, bucket, key, uploadID, parts, opts.ContentType)
}

// completedPart is one finished part: S3 wants the number and the ETag back in
// the completion body, in ascending order.
type completedPart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
	size       int64
}

// createMultipartUpload opens an upload and returns its ID.
func (c *Client) createMultipartUpload(ctx context.Context, bucket, key, contentType string) (string, error) {
	hdr := map[string]string{}
	if contentType != "" {
		hdr["Content-Type"] = contentType
	}

	var result struct {
		UploadID string `xml:"UploadId"`
	}
	req := request{
		method:      http.MethodPost,
		url:         c.url(bucket, key, url.Values{"uploads": {""}}),
		payloadHash: emptySHA256,
		header:      hdr,
	}
	err := c.do(ctx, req, func(resp *http.Response) error {
		return xml.NewDecoder(resp.Body).Decode(&result)
	})
	if err != nil {
		return "", fmt.Errorf("starting multipart upload of %s/%s: %w", bucket, key, err)
	}
	if result.UploadID == "" {
		return "", fmt.Errorf("starting multipart upload of %s/%s: server returned no upload ID", bucket, key)
	}
	return result.UploadID, nil
}

// partUploader carries the state uploadParts shares with its workers. It is a
// struct rather than a pile of closure variables because the read loop, the
// worker body and the failure path all touch the same four fields, and a
// mutex-guarded field is easier to reason about when its lock is next to it.
type partUploader struct {
	client *Client
	bucket string
	key    string
	upload string

	// slots bounds how many parts are in flight, and with them how many
	// part-sized buffers are live: the working set stays PartSize ×
	// Concurrency however large the object is.
	slots  chan struct{}
	wg     sync.WaitGroup
	cancel context.CancelFunc

	mu    sync.Mutex
	parts []completedPart
	err   error
}

// fail records the first error and stops the other workers: their parts are
// about to be aborted anyway, and a failing upload should not keep pushing
// bytes at the server.
func (u *partUploader) fail(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.err == nil {
		u.err = err
		u.cancel()
	}
}

// send uploads one part in the background, blocking first until a slot frees
// up. It reports false if the upload has already failed, which ends the read
// loop.
func (u *partUploader) send(ctx context.Context, number int, body []byte) bool {
	select {
	case u.slots <- struct{}{}:
	case <-ctx.Done():
		u.fail(ctx.Err())
		return false
	}

	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		defer func() { <-u.slots }()

		etag, err := u.client.uploadPart(ctx, u.bucket, u.key, u.upload, number, body)
		if err != nil {
			u.fail(err)
			return
		}
		u.mu.Lock()
		u.parts = append(u.parts, completedPart{PartNumber: number, ETag: etag, size: int64(len(body))})
		u.mu.Unlock()
	}()
	return true
}

// uploadParts reads r into part-sized buffers and uploads them with up to
// opts.Concurrency workers.
//
// Reading is sequential and single-threaded because the source may be a pipe
// and a pipe can only be read forwards; only the uploads overlap.
func (c *Client) uploadParts(ctx context.Context, bucket, key, uploadID string,
	r io.Reader, opts UploadOptions) ([]completedPart, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	u := &partUploader{
		client: c, bucket: bucket, key: key, upload: uploadID,
		slots:  make(chan struct{}, opts.Concurrency),
		cancel: cancel,
	}

	for number := 1; ; number++ {
		if number > maxParts {
			u.fail(fmt.Errorf("object needs more than %d parts at a part size of %d bytes; raise --part-size",
				maxParts, opts.PartSize))
			break
		}

		buf := make([]byte, opts.PartSize)
		n, readErr := io.ReadFull(r, buf)
		if n > 0 && !u.send(ctx, number, buf[:n]) {
			break
		}
		if readErr != nil {
			// ErrUnexpectedEOF is the short final part, not a failure.
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
				u.fail(fmt.Errorf("reading upload source: %w", readErr))
			}
			break
		}
	}

	u.wg.Wait()
	if u.err != nil {
		return nil, u.err
	}
	sort.Slice(u.parts, func(i, j int) bool { return u.parts[i].PartNumber < u.parts[j].PartNumber })
	return u.parts, nil
}

// uploadPart sends one part and returns its ETag.
func (c *Client) uploadPart(ctx context.Context, bucket, key, uploadID string,
	number int, body []byte) (string, error) {
	q := url.Values{"partNumber": {strconv.Itoa(number)}, "uploadId": {uploadID}}
	var etag string
	req := request{
		method:      http.MethodPut,
		url:         c.url(bucket, key, q),
		body:        bytes.NewReader(body),
		payloadHash: hexSHA256(body),
		size:        int64(len(body)),
	}
	err := c.do(ctx, req, func(resp *http.Response) error {
		etag = resp.Header.Get("ETag")
		if etag == "" {
			return fmt.Errorf("part %d: server returned no ETag", number)
		}
		return drainBody(resp)
	})
	if err != nil {
		return "", fmt.Errorf("uploading part %d of %s/%s: %w", number, bucket, key, err)
	}
	return etag, nil
}

// completeMultipartUpload assembles the parts into the finished object.
func (c *Client) completeMultipartUpload(ctx context.Context, bucket, key, uploadID string,
	parts []completedPart, contentType string) (*ObjectInfo, error) {
	// An upload with no parts at all cannot be completed — S3 rejects an empty
	// part list — so a zero-length source becomes a zero-length single PUT.
	if len(parts) == 0 {
		if err := c.abortMultipartUpload(ctx, bucket, key, uploadID); err != nil {
			return nil, err
		}
		return c.PutObject(ctx, bucket, key, bytes.NewReader(nil), 0, contentType)
	}

	body, err := xml.Marshal(struct {
		XMLName xml.Name        `xml:"CompleteMultipartUpload"`
		Parts   []completedPart `xml:"Part"`
	}{Parts: parts})
	if err != nil {
		return nil, fmt.Errorf("encoding multipart completion: %w", err)
	}

	var total int64
	for _, p := range parts {
		total += p.size
	}

	var result struct {
		ETag string `xml:"ETag"`
	}
	req := request{
		method:      http.MethodPost,
		url:         c.url(bucket, key, url.Values{"uploadId": {uploadID}}),
		body:        bytes.NewReader(body),
		payloadHash: hexSHA256(body),
		size:        int64(len(body)),
		header:      map[string]string{"Content-Type": "application/xml"},
	}
	// A completion can fail *inside* a 200 response: S3 keeps the connection
	// open while it assembles the object and then writes an <Error> document.
	// Decoding into a struct that only knows about ETag would swallow it.
	err = c.do(ctx, req, func(resp *http.Response) error {
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		if readErr != nil {
			return fmt.Errorf("reading multipart completion: %w", readErr)
		}
		if code := errorCodeInBody(payload); code != "" {
			return &APIError{
				StatusCode: resp.StatusCode,
				Code:       code,
				Message:    "multipart completion was refused after the request was accepted",
				Method:     http.MethodPost,
				Path:       req.url.Path,
			}
		}
		return xml.Unmarshal(payload, &result)
	})
	if err != nil {
		return nil, fmt.Errorf("completing multipart upload of %s/%s: %w", bucket, key, err)
	}

	return &ObjectInfo{
		Bucket:      bucket,
		Key:         key,
		Size:        total,
		ETag:        strings.Trim(result.ETag, `"`),
		ContentType: contentType,
	}, nil
}

// abortMultipartUpload discards an upload and the parts already stored.
func (c *Client) abortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error {
	// The parent context may already be cancelled — that is the usual reason
	// this is being called — so the abort gets a context of its own, or it
	// would fail without being sent and leak the parts.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortTimeout)
	defer cancel()

	req := request{
		method:      http.MethodDelete,
		url:         c.url(bucket, key, url.Values{"uploadId": {uploadID}}),
		payloadHash: emptySHA256,
	}
	if err := c.do(ctx, req, drainBody); err != nil {
		return fmt.Errorf("aborting multipart upload of %s/%s: %w", bucket, key, err)
	}
	return nil
}

// errorCodeInBody reports the <Code> of an S3 <Error> document, or "" if the
// payload is not one.
func errorCodeInBody(payload []byte) string {
	var doc struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
	}
	if xml.Unmarshal(payload, &doc) != nil {
		return ""
	}
	if doc.Code == "" && doc.XMLName.Local == "Error" {
		return "InternalError"
	}
	return doc.Code
}
