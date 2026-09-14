package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// multipartRecorder is a mock S3 that speaks the three-call multipart protocol
// and records what it was sent.
type multipartRecorder struct {
	mu sync.Mutex

	created  int
	aborted  int
	complete string // the completion body
	parts    map[int]string
	failPart int // a part number to refuse, 0 for none
}

func newMultipartRecorder() *multipartRecorder {
	return &multipartRecorder{parts: map[int]string{}}
}

func (m *multipartRecorder) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		body, _ := io.ReadAll(r.Body)

		m.mu.Lock()
		defer m.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && q.Has("uploads"):
			m.created++
			_, _ = fmt.Fprint(w, `<InitiateMultipartUploadResult><UploadId>up-1</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut && q.Has("partNumber"):
			n, _ := strconv.Atoi(q.Get("partNumber"))
			if n == m.failPart {
				w.WriteHeader(http.StatusForbidden)
				_, _ = fmt.Fprint(w, `<Error><Code>AccessDenied</Code></Error>`)
				return
			}
			m.parts[n] = string(body)
			w.Header().Set("ETag", fmt.Sprintf(`"etag-%d"`, n))
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && q.Has("uploadId"):
			m.complete = string(body)
			_, _ = fmt.Fprint(w, `<CompleteMultipartUploadResult><ETag>"final-etag"</ETag></CompleteMultipartUploadResult>`)
		case r.Method == http.MethodDelete && q.Has("uploadId"):
			m.aborted++
			w.WriteHeader(http.StatusNoContent)
		default:
			// A plain PUT: the single-part path.
			m.parts[0] = string(body)
			w.Header().Set("ETag", `"single"`)
			w.WriteHeader(http.StatusOK)
		}
	}
}

// unseekable hides the io.Seeker a bytes.Reader would otherwise offer, which is
// what a pipe looks like to the client.
type unseekable struct{ r io.Reader }

func (u unseekable) Read(p []byte) (int, error) { return u.r.Read(p) }

// A source whose length is unknown — stdin — must take the multipart path even
// when it is tiny, because a single signed PUT has to hash its body first and a
// pipe cannot be rewound.
func TestPutObjectStreamUsesMultipartForAnUnknownSize(t *testing.T) {
	rec := newMultipartRecorder()
	c := newTestClient(t, rec.handler(t))

	obj, err := c.PutObjectStream(context.Background(), "b", "k",
		unseekable{strings.NewReader("small")},
		UploadOptions{Size: -1, PartSize: MinPartSize, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if rec.created != 1 {
		t.Errorf("multipart uploads created = %d, want 1", rec.created)
	}
	if got := rec.parts[1]; got != "small" {
		t.Errorf("part 1 = %q, want small", got)
	}
	if obj.ETag != "final-etag" {
		t.Errorf("ETag = %q, want final-etag", obj.ETag)
	}
	if obj.Size != 5 {
		t.Errorf("Size = %d, want 5", obj.Size)
	}
}

// A known, small, seekable source is one request instead of three.
func TestPutObjectStreamUsesASinglePutWhenItFits(t *testing.T) {
	rec := newMultipartRecorder()
	c := newTestClient(t, rec.handler(t))

	obj, err := c.PutObjectStream(context.Background(), "b", "k",
		bytes.NewReader([]byte("payload")),
		UploadOptions{Size: 7, PartSize: MinPartSize, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if rec.created != 0 {
		t.Errorf("a multipart upload was started for a %d-byte object", 7)
	}
	if obj.ETag != "single" {
		t.Errorf("ETag = %q, want single", obj.ETag)
	}
}

// Several parts must arrive whole, in the right pieces, and be listed in
// ascending order in the completion body — S3 rejects any other order.
func TestPutObjectStreamSplitsAndOrdersParts(t *testing.T) {
	rec := newMultipartRecorder()
	c := newTestClient(t, rec.handler(t))

	// Three parts: two full and a short last one.
	payload := strings.Repeat("a", MinPartSize) + strings.Repeat("b", MinPartSize) + "tail"
	obj, err := c.PutObjectStream(context.Background(), "b", "k",
		unseekable{strings.NewReader(payload)},
		UploadOptions{Size: -1, PartSize: MinPartSize, Concurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(rec.parts); got != 3 {
		t.Fatalf("parts = %d, want 3", got)
	}
	if len(rec.parts[1]) != MinPartSize || len(rec.parts[2]) != MinPartSize || rec.parts[3] != "tail" {
		t.Errorf("part sizes = %d/%d/%q, want two full parts and a short tail",
			len(rec.parts[1]), len(rec.parts[2]), rec.parts[3])
	}
	if obj.Size != int64(len(payload)) {
		t.Errorf("Size = %d, want %d", obj.Size, len(payload))
	}
	// Reassembled, the parts are the original stream.
	var nums []int
	for n := range rec.parts {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	var joined strings.Builder
	for _, n := range nums {
		joined.WriteString(rec.parts[n])
	}
	if joined.String() != payload {
		t.Error("the parts do not reassemble into the source stream")
	}
	// The completion body must name the parts in ascending order.
	if !strings.Contains(rec.complete, "<PartNumber>1</PartNumber>") ||
		strings.Index(rec.complete, "<PartNumber>1</PartNumber>") >
			strings.Index(rec.complete, "<PartNumber>3</PartNumber>") {
		t.Errorf("completion body is not in ascending part order:\n%s", rec.complete)
	}
}

// A failed part must abort the upload: a half-written object must never become
// visible, and the stored parts must stop costing money.
func TestPutObjectStreamAbortsOnFailure(t *testing.T) {
	rec := newMultipartRecorder()
	rec.failPart = 2
	c := newTestClient(t, rec.handler(t))

	payload := strings.Repeat("a", MinPartSize) + strings.Repeat("b", MinPartSize)
	_, err := c.PutObjectStream(context.Background(), "b", "k",
		unseekable{strings.NewReader(payload)},
		UploadOptions{Size: -1, PartSize: MinPartSize, Concurrency: 1})
	if err == nil {
		t.Fatal("a refused part was reported as success")
	}
	if !strings.Contains(err.Error(), "part 2") {
		t.Errorf("error %q does not name the failing part", err)
	}
	if rec.aborted != 1 {
		t.Errorf("aborts = %d, want 1 — the parts already stored would otherwise leak", rec.aborted)
	}
	if rec.complete != "" {
		t.Error("the upload was completed despite a failed part")
	}
}

// S3 can refuse a completion inside a 200 response: it holds the connection
// open while it assembles the object, then writes an <Error> document. Decoding
// only the fields we want would swallow it and report a successful upload.
func TestPutObjectStreamDetectsAnErrorInsideA200(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Has("uploads"):
			_, _ = fmt.Fprint(w, `<InitiateMultipartUploadResult><UploadId>up-1</UploadId></InitiateMultipartUploadResult>`)
		case q.Has("partNumber"):
			w.Header().Set("ETag", `"e"`)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `<Error><Code>InternalError</Code><Message>assembly failed</Message></Error>`)
		}
	})

	_, err := c.PutObjectStream(context.Background(), "b", "k",
		unseekable{strings.NewReader("payload")},
		UploadOptions{Size: -1, PartSize: MinPartSize, Concurrency: 1})
	if err == nil {
		t.Fatal("an <Error> inside a 200 was reported as a successful upload")
	}
	if got := ErrorCode(err); got != "InternalError" {
		t.Errorf("ErrorCode = %q, want InternalError", got)
	}
}

// A zero-length stream cannot be completed as a multipart upload — S3 rejects
// an empty part list — so it has to fall back to a plain PUT.
func TestPutObjectStreamHandlesAnEmptyStream(t *testing.T) {
	rec := newMultipartRecorder()
	c := newTestClient(t, rec.handler(t))

	obj, err := c.PutObjectStream(context.Background(), "b", "k",
		unseekable{strings.NewReader("")},
		UploadOptions{Size: -1, PartSize: MinPartSize, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if obj.Size != 0 {
		t.Errorf("Size = %d, want 0", obj.Size)
	}
	if rec.aborted != 1 {
		t.Errorf("aborts = %d, want the empty upload discarded", rec.aborted)
	}
	if rec.complete != "" {
		t.Error("an empty part list was sent to CompleteMultipartUpload")
	}
}

// A part size the protocol forbids must be refused before anything is uploaded,
// not silently raised to the minimum.
func TestUploadOptionsRejectATooSmallPartSize(t *testing.T) {
	rec := newMultipartRecorder()
	c := newTestClient(t, rec.handler(t))

	_, err := c.PutObjectStream(context.Background(), "b", "k", strings.NewReader("x"),
		UploadOptions{Size: -1, PartSize: 1024})
	if err == nil || !strings.Contains(err.Error(), "at least") {
		t.Errorf("err = %v, want it to name S3's minimum part size", err)
	}
	if rec.created != 0 {
		t.Error("an upload was started despite invalid options")
	}
}

func TestUploadOptionsDefaults(t *testing.T) {
	got, err := UploadOptions{Size: -1}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.PartSize != DefaultPartSize {
		t.Errorf("PartSize = %d, want %d", got.PartSize, DefaultPartSize)
	}
	if got.Concurrency != DefaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", got.Concurrency, DefaultConcurrency)
	}
	if _, err := (UploadOptions{Concurrency: -1}).withDefaults(); err == nil {
		t.Error("a negative concurrency was accepted")
	}
}
