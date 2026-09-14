package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// MaxDeleteBatch is the protocol's limit on keys per DeleteObjects call.
const MaxDeleteBatch = 1000

// DeleteTarget is one key, optionally one specific version of it, for a batch
// delete.
type DeleteTarget struct {
	Key       string
	VersionID string
}

// DeleteError is one key a batch delete refused. S3 reports these inside a
// 200 response, so they are returned rather than raised: the rest of the batch
// did happen.
type DeleteError struct {
	Key       string
	VersionID string
	Code      string
	Message   string
}

func (f DeleteError) Error() string {
	if f.Message != "" {
		return fmt.Sprintf("%s: %s (%s)", f.Key, f.Message, f.Code)
	}
	return fmt.Sprintf("%s: %s", f.Key, f.Code)
}

// DeleteObjects removes up to MaxDeleteBatch keys in one request, which is what
// makes emptying a bucket of a million objects finish: it is a thousandth of the
// requests one-DELETE-per-key costs.
//
// The returned slice names the keys the server refused. A nil error with a
// non-empty slice is the normal partial-failure case, not a contradiction —
// S3 answers 200 and itemises what it would not do.
func (c *Client) DeleteObjects(ctx context.Context, bucket string, targets []DeleteTarget) ([]DeleteError, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	if len(targets) > MaxDeleteBatch {
		return nil, fmt.Errorf("a batch delete takes at most %d keys, got %d", MaxDeleteBatch, len(targets))
	}

	// Field-for-field a DeleteTarget, so one converts to the other; the tags
	// are what this copy exists for.
	type object struct {
		Key       string `xml:"Key"`
		VersionID string `xml:"VersionId,omitempty"`
	}
	doc := struct {
		XMLName xml.Name `xml:"Delete"`
		Quiet   bool     `xml:"Quiet"`
		Objects []object `xml:"Object"`
	}{
		// Quiet suppresses a success entry per key; only the failures come
		// back, which is all this call reports.
		Quiet:   true,
		Objects: make([]object, len(targets)),
	}
	for i, t := range targets {
		doc.Objects[i] = object(t)
	}

	body, err := xml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encoding batch delete: %w", err)
	}

	var result struct {
		Errors []struct {
			Key       string `xml:"Key"`
			VersionID string `xml:"VersionId"`
			Code      string `xml:"Code"`
			Message   string `xml:"Message"`
		} `xml:"Error"`
	}
	req := request{
		method:      http.MethodPost,
		url:         c.url(bucket, "", url.Values{"delete": {""}}),
		body:        bytes.NewReader(body),
		payloadHash: hexSHA256(body),
		size:        int64(len(body)),
		header: map[string]string{
			"Content-Type": "application/xml",
			// AWS refuses a batch delete without it; see contentMD5.
			"Content-MD5": contentMD5(body),
		},
	}
	err = c.do(ctx, req, func(resp *http.Response) error {
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		if readErr != nil {
			return fmt.Errorf("reading batch delete result: %w", readErr)
		}
		return xml.Unmarshal(payload, &result)
	})
	if err != nil {
		return nil, fmt.Errorf("deleting %d objects from %s: %w", len(targets), bucket, err)
	}

	var failures []DeleteError
	for _, e := range result.Errors {
		failures = append(failures, DeleteError{
			Key: e.Key, VersionID: e.VersionID, Code: e.Code, Message: e.Message,
		})
	}
	return failures, nil
}
