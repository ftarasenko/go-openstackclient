package image

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imagedata"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imageimport"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

// Data is an image's data, to upload or stage.
type Data struct {
	// Body is read once, to the end. If it is also an io.Seeker, gophercloud
	// rewinds it to replay a request after re-authenticating.
	Body io.Reader
	// Size, when positive, is sent as X-OpenStack-Image-Size, which glance
	// (2025.2 and later) uses to size the store up front, as openstacksdk does.
	Size int64
	// GlanceDirect makes CreateImageWithData stage the data and import it with
	// the glance-direct method instead of uploading it directly.
	GlanceDirect bool
}

// ErrGlanceDirectUnavailable is CreateImageWithData's answer when
// Data.GlanceDirect is set and the endpoint does not offer the
// glance-direct import method.
var ErrGlanceDirectUnavailable = errors.New("the cloud does not offer the glance-direct import method")

// CreateImage creates an image record, with no data yet. It also returns the
// import methods the endpoint advertises for it (glance's
// OpenStack-image-import-methods header), empty when it advertises none.
func CreateImage(ctx context.Context, c *gophercloud.ServiceClient, opts images.CreateOpts) (*images.Image, []string, error) {
	res := images.Create(ctx, c, opts)
	img, err := res.Extract()
	if err != nil {
		return nil, nil, fmt.Errorf("creating image %q: %w", opts.Name, err)
	}
	var methods []string
	if h := res.Header.Get("OpenStack-image-import-methods"); h != "" {
		methods = strings.Split(h, ",")
	}
	return img, methods, nil
}

// CreateImageWithData creates an image record and, when data is non-nil,
// fills it — by direct upload, or by staging and a glance-direct import — and
// returns the image as glance reports it afterwards. If filling it fails, the
// record is deleted again, as openstacksdk does, rather than left queued; the
// error says whether that worked.
func CreateImageWithData(ctx context.Context, c *gophercloud.ServiceClient, opts images.CreateOpts, data *Data) (*images.Image, error) {
	img, methods, err := CreateImage(ctx, c, opts)
	if err != nil || data == nil {
		return img, err
	}
	if err := fill(ctx, c, img.ID, data, methods); err != nil {
		return nil, DiscardImage(ctx, c, img.ID, err)
	}
	// Re-fetch so the record reflects the resulting status, size and checksum.
	if img, err = images.Get(ctx, c, img.ID).Extract(); err != nil {
		return nil, fmt.Errorf("getting image after upload: %w", err)
	}
	return img, nil
}

func fill(ctx context.Context, c *gophercloud.ServiceClient, id string, data *Data, methods []string) error {
	if !data.GlanceDirect {
		return UploadImageData(ctx, c, id, *data)
	}
	if !slices.Contains(methods, string(imageimport.GlanceDirectMethod)) {
		return ErrGlanceDirectUnavailable
	}
	if err := StageImageData(ctx, c, id, *data); err != nil {
		return err
	}
	return ImportImage(ctx, c, id, ImportOpts{CreateOpts: imageimport.CreateOpts{Name: imageimport.GlanceDirectMethod}})
}

// UploadImageData uploads an image's data (PUT /v2/images/{id}/file).
func UploadImageData(ctx context.Context, c *gophercloud.ServiceClient, id string, data Data) error {
	if err := putImageData(ctx, c, id, "file", data); err != nil {
		return fmt.Errorf("uploading image data for %s: %w", id, err)
	}
	return nil
}

// StageImageData uploads an image's data to glance's staging area
// (PUT /v2/images/{id}/stage), where an import with the glance-direct method
// picks it up. The image stays "uploading" until then.
func StageImageData(ctx context.Context, c *gophercloud.ServiceClient, id string, data Data) error {
	if err := putImageData(ctx, c, id, "stage", data); err != nil {
		return fmt.Errorf("staging image data for %s: %w", id, err)
	}
	return nil
}

// putImageData is imagedata.Upload/Stage plus the X-OpenStack-Image-Size
// header openstacksdk sends.
func putImageData(ctx context.Context, c *gophercloud.ServiceClient, id, part string, data Data) error {
	headers := map[string]string{"Content-Type": "application/octet-stream"}
	if data.Size > 0 {
		headers["X-OpenStack-Image-Size"] = strconv.FormatInt(data.Size, 10)
	}
	resp, err := c.Put(ctx, c.ServiceURL("images", id, part), data.Body, nil, &gophercloud.RequestOpts{
		MoreHeaders: headers,
		OkCodes:     []int{http.StatusNoContent},
	})
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err
}

// DiscardImage deletes an image whose data could not be filled in, even if
// ctx was cancelled (with a 30-second bound of its own), and returns cause
// annotated with the outcome.
func DiscardImage(ctx context.Context, c *gophercloud.ServiceClient, id string, cause error) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := images.Delete(cctx, c, id).ExtractErr(); err != nil {
		return fmt.Errorf("%w (deleting the partial image also failed: %w)", cause, err)
	}
	return fmt.Errorf("%w (the partial image was deleted)", cause)
}

// DownloadImage opens an image's data for reading. The caller closes it.
func DownloadImage(ctx context.Context, c *gophercloud.ServiceClient, id string) (io.ReadCloser, error) {
	body, err := imagedata.Download(ctx, c, id).Extract()
	if err != nil {
		return nil, fmt.Errorf("downloading image %s: %w", id, err)
	}
	return body, nil
}

// CreateImageFromVolume uploads a cinder volume to glance as a new image,
// through cinder's os-volume_upload_image action — so volumeClient is a
// volume (block-storage) client, not an image one. opts.Visibility and
// opts.Protected need volume microversion 3.1 or later.
func CreateImageFromVolume(ctx context.Context, volumeClient *gophercloud.ServiceClient, volumeID string,
	opts volumes.UploadImageOpts,
) (volumes.VolumeImage, error) {
	vi, err := volumes.UploadImage(ctx, volumeClient, volumeID, opts).Extract()
	if err != nil {
		return vi, fmt.Errorf("creating image %q from volume %s: %w", opts.ImageName, volumeID, err)
	}
	return vi, nil
}
