package s3

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// CopyObject copies an object inside the store, without the bytes travelling
// through koc. The source is named in a header rather than a body, so a 100 GiB
// copy costs one small request.
//
// versionID selects a specific version of the source, or "" for the current one.
// contentType, when empty, is carried over from the source along with the rest
// of its metadata (S3's default COPY directive).
func (c *Client) CopyObject(ctx context.Context, src, dst ObjectRef, versionID, contentType string) (*ObjectInfo, error) {
	// The copy source is a path, so its slashes must survive encoding while
	// everything else in the key is escaped — the same rule as a request URI.
	source := "/" + src.Bucket + "/" + strings.TrimPrefix(src.Key, "/")
	if versionID != "" {
		source += "?versionId=" + uriEncode(versionID, false)
	}

	hdr := map[string]string{"x-amz-copy-source": uriEncode(source, true)}
	if contentType != "" {
		hdr["Content-Type"] = contentType
		// Without this S3 keeps the source's metadata and ignores the header.
		hdr["x-amz-metadata-directive"] = "REPLACE"
	}

	var result struct {
		ETag         string `xml:"ETag"`
		LastModified string `xml:"LastModified"`
	}
	req := request{
		method:      http.MethodPut,
		url:         c.url(dst.Bucket, dst.Key, nil),
		payloadHash: emptySHA256,
		header:      hdr,
	}
	// Like a multipart completion, a copy can fail inside a 200: the server
	// holds the connection open while it copies and then writes an <Error>.
	err := c.do(ctx, req, func(resp *http.Response) error {
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		if readErr != nil {
			return fmt.Errorf("reading copy result: %w", readErr)
		}
		if code := errorCodeInBody(payload); code != "" {
			return &APIError{
				StatusCode: resp.StatusCode,
				Code:       code,
				Message:    "copy was refused after the request was accepted",
				Method:     http.MethodPut,
				Path:       req.url.Path,
			}
		}
		return xml.Unmarshal(payload, &result)
	})
	if err != nil {
		return nil, fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}

	return &ObjectInfo{
		Bucket:       dst.Bucket,
		Key:          dst.Key,
		ETag:         strings.Trim(result.ETag, `"`),
		LastModified: parseS3Time(result.LastModified),
		ContentType:  contentType,
	}, nil
}

// ObjectRef is a bucket/key pair. It exists so CopyObject's four string
// arguments cannot be transposed into a copy in the wrong direction.
type ObjectRef struct {
	Bucket string
	Key    string
}

func (r ObjectRef) String() string { return r.Bucket + "/" + r.Key }
