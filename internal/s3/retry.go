package s3

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Retry policy. A koc S3 call can be a multi-gigabyte transfer over a cluster
// network, where a reset connection or a momentary 503 from one gateway node is
// routine rather than exceptional — s5cmd retries ten times by default for the
// same reason. Without this a transient blip failed the whole command and, on an
// upload, left the object half-written.
const (
	// retryBaseDelay is the first backoff interval; each further attempt doubles
	// it up to retryMaxDelay.
	retryBaseDelay = 200 * time.Millisecond
	retryMaxDelay  = 10 * time.Second
)

// retryableCodes are the S3 error codes that mean "ask again", as opposed to
// "you asked wrong". SlowDown and RequestTimeout are the two a healthy store
// still emits under load. RequestTimeTooSkewed is deliberately absent: it is a
// clock problem, and repeating the request cannot fix it.
var retryableCodes = map[string]bool{
	"InternalError":      true,
	"ServiceUnavailable": true,
	"SlowDown":           true,
	"RequestTimeout":     true,
}

// nonRetryableError marks an error raised after the response headers were accepted,
// i.e. by the sink while it consumed the body. Those are never replayed: the
// sink has already written some of the object to a file or to stdout, and a
// second attempt would append a duplicate prefix rather than resume.
type nonRetryableError struct{ err error }

func (e *nonRetryableError) Error() string { return e.err.Error() }
func (e *nonRetryableError) Unwrap() error { return e.err }

// retryable reports whether err is worth another attempt.
func retryable(err error) bool {
	var sink *nonRetryableError
	if errors.As(err, &sink) {
		return false
	}

	var ae *APIError
	if !errors.As(err, &ae) {
		// Not an answer from S3 at all: a dial failure, a reset, or EOF before
		// the headers arrived. Nothing was applied, so it is safe to repeat.
		return true
	}
	if retryableCodes[ae.Code] {
		return true
	}
	switch ae.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return ae.StatusCode >= http.StatusInternalServerError
}

// backoff waits before attempt n (1 = the first retry), or returns the
// context's error if the caller gave up first. The delay carries a little
// jitter so a pool of --concurrency workers that all hit the same 503 do not
// all come back in the same millisecond; it is read off the clock rather than
// from math/rand because spreading requests is the only property needed and an
// unseeded PRNG in a short-lived CLI gives no better one.
func (c *Client) backoff(ctx context.Context, n int) error {
	d := c.retryBase
	for range n - 1 {
		if d *= 2; d >= retryMaxDelay {
			d = retryMaxDelay
			break
		}
	}
	if spread := int64(d / 4); spread > 0 {
		d += time.Duration(c.now().UnixNano() % spread)
	}

	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
