package image

import (
	"context"
	"fmt"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

// WaitForStatus polls an image every interval until its status is want — an
// import moving it to active, say — and returns it as last read. It gives up
// when ctx is done (give it a deadline), when a read fails, and when the image
// reaches a status it cannot leave for want: killed, deleted or
// pending_delete.
//
// This is the shape of every koc wait helper: the caller's context bounds it,
// and the interval is the caller's choice, not a hidden default.
func WaitForStatus(ctx context.Context, c *gophercloud.ServiceClient, id string, want images.ImageStatus,
	interval time.Duration,
) (*images.Image, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var last *images.Image
	timedOut := func() (*images.Image, error) {
		return last, fmt.Errorf("waiting for image %s to be %s (still %s): %w", id, want, last.Status, ctx.Err())
	}
	for {
		img, err := GetImage(ctx, c, id)
		if err != nil {
			// The deadline landing mid-read is the wait timing out, not a
			// failed read.
			if last != nil && ctx.Err() != nil {
				return timedOut()
			}
			return last, err
		}
		last = img
		switch img.Status {
		case want:
			return img, nil
		case images.ImageStatusKilled, images.ImageStatusDeleted, images.ImageStatusPendingDelete:
			return img, fmt.Errorf("waiting for image %s to be %s: it is %s", id, want, img.Status)
		}
		select {
		case <-ctx.Done():
			return timedOut()
		case <-ticker.C:
		}
	}
}
