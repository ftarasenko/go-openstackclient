//go:build !koc_embedded

package watch

import (
	"errors"

	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// s3Status is httpStatus for internal/s3's error shape. A koc_embedded build
// has no S3 client, so it links none of it (errors_s3_embedded.go).
func s3Status(err error) (int, bool) {
	var apiErr *s3.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode, true
	}
	return 0, false
}
