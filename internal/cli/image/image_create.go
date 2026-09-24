package image

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imageimport"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Upstream OSC's defaults and choices (openstackclient/image/v2/image.py).
const (
	defaultDiskFormat      = "raw"
	defaultContainerFormat = "bare"
)

var (
	diskFormats      = []string{"ami", "ari", "aki", "vhd", "vmdk", "raw", "qcow2", "vhdx", "vdi", "iso", "ploop"}
	containerFormats = []string{"ami", "ari", "aki", "bare", "docker", "ova", "ovf"}
	// imageV1Options are accepted but rejected, as upstream does.
	imageV1Options = []string{"location", "copy-from", "checksum", "store"}
	// volumeIgnoredOptions have no effect with --volume; upstream warns about them.
	volumeIgnoredOptions = []string{"id", "min-disk", "min-ram", "progress", "property", "tag", "project", "import"}
)

// imageCreateFlags holds the attributes accepted by "image create".
//
// Flag names follow upstream OSC (`openstack image create`). UNVERIFIED against
// the KeyStack reference (docs.keystack.ru returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics. Not implemented:
// --sign-key-path / --sign-cert-id.
type imageCreateFlags struct {
	diskFormat      string
	containerFormat string
	file            string
	volume          string
	force           bool
	size            int64
	minDisk         int
	minRAM          int
	public          bool
	private         bool
	community       bool
	shared          bool
	protected       bool
	unprotected     bool
	property        []string
	project         string
	projectDomain   string
	useImport       bool
	progress        bool

	visibility string
	id         string
	tag        []string

	// owner is --project resolved to an ID by the command.
	owner string
}

// imageVisibilities are glance's four visibility values. --public, --private,
// --community and --shared are upstream's shorthands; --visibility is koc's.
var imageVisibilities = []string{
	string(images.ImageVisibilityPublic),
	string(images.ImageVisibilityPrivate),
	string(images.ImageVisibilityShared),
	string(images.ImageVisibilityCommunity),
}

// imageSource is the data uploaded after the image record is created.
type imageSource struct {
	r        io.Reader
	size     int64     // sent as X-OpenStack-Image-Size when > 0
	progress io.Writer // non-nil draws a progress bar
}

func newImageCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &imageCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create/upload an image (data from --file, --volume or piped stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if err := f.check(cmd); err != nil {
				return err
			}
			// Open the data before creating anything, so a bad path leaves no image.
			src, closeSrc, err := f.openSource(cmd)
			if err != nil {
				return err
			}
			defer closeSrc()
			ctx := cmd.Context()
			client, session, err := newImageSession(ctx, a)
			if err != nil {
				return err
			}
			if f.volume != "" {
				return createImageFromVolume(ctx, cmd, session, o, args[0], f)
			}
			if f.owner, err = resolveProjectRefInDomain(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			return runImageCreate(ctx, client, o, args[0], f, src, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.diskFormat, "disk-format", defaultDiskFormat, "disk format: "+strings.Join(diskFormats, ", "))
	fl.StringVar(&f.containerFormat, "container-format", defaultContainerFormat, "container format: "+strings.Join(containerFormats, ", "))
	fl.StringVar(&f.file, "file", "", "upload image data from this local file")
	fl.StringVar(&f.volume, "volume", "", "create the image from this volume (name or ID)")
	fl.BoolVar(&f.force, "force", false, "create the image even if the volume is in use (only with --volume)")
	fl.Int64Var(&f.size, "size", 0, "size of the image data in bytes (default: the file's size)")
	fl.IntVar(&f.minDisk, "min-disk", 0, "minimum disk size in GB required to boot the image")
	fl.IntVar(&f.minRAM, "min-ram", 0, "minimum RAM in MB required to boot the image")
	fl.BoolVar(&f.public, "public", false, "image is visible to all users")
	fl.BoolVar(&f.private, "private", false, "image is visible to the owner only")
	fl.BoolVar(&f.community, "community", false, "image is usable by all users but listed only for the owner")
	fl.BoolVar(&f.shared, "shared", false, "image is visible to the owner and image members")
	fl.BoolVar(&f.protected, "protected", false, "prevent the image from being deleted")
	fl.BoolVar(&f.unprotected, "unprotected", false, "allow the image to be deleted (default)")
	fl.StringArrayVar(&f.property, "property", nil, "arbitrary image property key=value (repeatable)")
	fl.StringVar(&f.project, "project", "", "owner project of the image (name or ID)")
	fl.StringVar(&f.projectDomain, "project-domain", "", "domain owning --project, to disambiguate the name (name or ID)")
	fl.BoolVar(&f.useImport, "import", false, "upload via the glance-direct import method instead of a direct upload")
	fl.BoolVar(&f.progress, "progress", false, "show upload progress on stderr (only with --file)")
	fl.StringVar(&f.visibility, "visibility", "", "image visibility: "+strings.Join(imageVisibilities, ", "))
	fl.StringVar(&f.id, "id", "", "create the image with this UUID instead of a server-assigned one")
	fl.StringArrayVar(&f.tag, "tag", nil, "image tag (repeatable)")
	for _, name := range imageV1Options {
		fl.String(name, "", "")
		_ = fl.MarkHidden(name)
	}
	cmd.MarkFlagsMutuallyExclusive("public", "private", "community", "shared", "visibility")
	cmd.MarkFlagsMutuallyExclusive("protected", "unprotected")
	cmd.MarkFlagsMutuallyExclusive("file", "volume")
	return cmd
}

// check rejects bad flag values before any file is opened or request sent.
func (f *imageCreateFlags) check(cmd *cobra.Command) error {
	for _, name := range imageV1Options {
		if cmd.Flags().Changed(name) {
			return fmt.Errorf("--%s is an Image v1 option that is no longer supported in Image v2", name)
		}
	}
	if !slices.Contains(diskFormats, f.diskFormat) {
		return fmt.Errorf("unsupported --disk-format %q: expected one of %s", f.diskFormat, strings.Join(diskFormats, ", "))
	}
	if !slices.Contains(containerFormats, f.containerFormat) {
		return fmt.Errorf("unsupported --container-format %q: expected one of %s", f.containerFormat, strings.Join(containerFormats, ", "))
	}
	if cmd.Flags().Changed("size") && f.size <= 0 {
		return errors.New("--size must be a positive integer")
	}
	if f.volume != "" {
		for _, name := range volumeIgnoredOptions {
			if cmd.Flags().Changed(name) {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "koc: --%s is ignored when creating an image from a volume\n", name)
			}
		}
	}
	return nil
}

// openSource returns the image data: --file, or stdin when it is a pipe or a
// redirected file. Upstream reads any non-terminal stdin; koc skips character
// devices, so `</dev/null` and cron jobs create an empty record rather than
// uploading zero bytes.
func (f *imageCreateFlags) openSource(cmd *cobra.Command) (*imageSource, func(), error) {
	noop := func() { /* nothing to close */ }
	var src *imageSource
	switch {
	case f.file != "":
		file, err := os.Open(f.file)
		if err != nil {
			return nil, noop, fmt.Errorf("opening image file %q: %w", f.file, err)
		}
		st, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, noop, fmt.Errorf("opening image file %q: %w", f.file, err)
		}
		src = &imageSource{r: file, size: st.Size()}
		if f.progress {
			src.progress = cmd.ErrOrStderr()
		}
		noop = func() { _ = file.Close() }
	case f.volume == "":
		src = stdinSource(cmd.InOrStdin())
	}
	if f.size > 0 {
		if src == nil {
			return nil, noop, errors.New("--size requires image data via --file or stdin")
		}
		src.size = f.size
	}
	return src, noop, nil
}

func stdinSource(in io.Reader) *imageSource {
	file, ok := in.(*os.File)
	if !ok {
		return &imageSource{r: in}
	}
	st, err := file.Stat()
	if err != nil {
		return nil
	}
	switch {
	case st.Mode().IsRegular():
		return &imageSource{r: file, size: st.Size()}
	case st.Mode()&os.ModeNamedPipe != 0:
		return &imageSource{r: file}
	}
	return nil
}

// resolveImageVisibility folds --visibility and the --public/--private shorthands
// into one optional value. nil means "say nothing", leaving glance's default
// (private on most deployments) in place rather than asserting it.
func resolveImageVisibility(visibility string, public, private bool) (*images.ImageVisibility, error) {
	switch {
	case visibility != "":
		if !slices.Contains(imageVisibilities, visibility) {
			return nil, fmt.Errorf("unsupported --visibility %q: expected one of %s",
				visibility, strings.Join(imageVisibilities, ", "))
		}
		v := images.ImageVisibility(visibility)
		return &v, nil
	case public:
		v := images.ImageVisibilityPublic
		return &v, nil
	case private:
		v := images.ImageVisibilityPrivate
		return &v, nil
	}
	return nil, nil
}

// resolvedVisibility adds the --community/--shared shorthands.
func (f *imageCreateFlags) resolvedVisibility() (*images.ImageVisibility, error) {
	v := f.visibility
	switch {
	case f.community:
		v = string(images.ImageVisibilityCommunity)
	case f.shared:
		v = string(images.ImageVisibilityShared)
	}
	return resolveImageVisibility(v, f.public, f.private)
}

func (f *imageCreateFlags) protectedValue() *bool {
	switch {
	case f.protected, f.unprotected:
		v := f.protected
		return &v
	}
	return nil
}

func (f *imageCreateFlags) createOpts(name string) (images.CreateOpts, error) {
	props, err := parseKeyValMap(f.property)
	if err != nil {
		return images.CreateOpts{}, fmt.Errorf("parsing --property: %w", err)
	}
	if f.owner != "" {
		if props == nil {
			props = map[string]string{}
		}
		props["owner"] = f.owner
	}
	visibility, err := f.resolvedVisibility()
	if err != nil {
		return images.CreateOpts{}, err
	}
	return images.CreateOpts{
		Name:            name,
		ID:              f.id,
		DiskFormat:      cmp.Or(f.diskFormat, defaultDiskFormat),
		ContainerFormat: cmp.Or(f.containerFormat, defaultContainerFormat),
		MinDisk:         f.minDisk,
		MinRAM:          f.minRAM,
		Protected:       f.protectedValue(),
		Properties:      props,
		Tags:            f.tag,
		Visibility:      visibility,
	}, nil
}

// runImageCreate creates the record, uploads src (if any) and shows the result.
// A failed upload deletes the record, as openstacksdk does, instead of leaving
// it queued.
func runImageCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name string, f *imageCreateFlags, src *imageSource, w io.Writer,
) error {
	opts, err := f.createOpts(name)
	if err != nil {
		return err
	}
	res := images.Create(ctx, client, opts)
	img, err := res.Extract()
	if err != nil {
		return fmt.Errorf("creating image %q: %w", name, err)
	}
	if src != nil {
		if err := uploadImageData(ctx, client, img.ID, src, f.useImport, res.Header.Get("OpenStack-image-import-methods")); err != nil {
			return discardImage(ctx, client, img.ID, err)
		}
		// Re-fetch so the shown record reflects the resulting status/size/checksum.
		if img, err = images.Get(ctx, client, img.ID).Extract(); err != nil {
			return fmt.Errorf("getting image after upload: %w", err)
		}
	}
	fields, values := imageShowFields(img)
	return o.WriteSingle(w, fields, values)
}

func uploadImageData(ctx context.Context, client *gophercloud.ServiceClient, id string,
	src *imageSource, useImport bool, importMethods string,
) error {
	if !useImport {
		if err := putImageData(ctx, client, id, "file", src); err != nil {
			return fmt.Errorf("uploading image data for %s: %w", id, err)
		}
		return nil
	}
	if !slices.Contains(strings.Split(importMethods, ","), string(imageimport.GlanceDirectMethod)) {
		return errors.New("--import: the cloud does not offer the glance-direct import method")
	}
	if err := putImageData(ctx, client, id, "stage", src); err != nil {
		return fmt.Errorf("staging image data for %s: %w", id, err)
	}
	opts := imageimport.CreateOpts{Name: imageimport.GlanceDirectMethod}
	if err := imageimport.Create(ctx, client, id, opts).ExtractErr(); err != nil {
		return fmt.Errorf("importing image %s: %w", id, err)
	}
	return nil
}

// putImageData is imagedata.Upload/Stage plus the X-OpenStack-Image-Size header
// openstacksdk sends, which glance (2025.2+) uses to size the store up front.
func putImageData(ctx context.Context, client *gophercloud.ServiceClient, id, part string, src *imageSource) error {
	headers := map[string]string{"Content-Type": "application/octet-stream"}
	if src.size > 0 {
		headers["X-OpenStack-Image-Size"] = strconv.FormatInt(src.size, 10)
	}
	body := src.r
	if src.progress != nil && src.size > 0 {
		body = &progressReader{r: src.r, total: src.size, w: src.progress, last: -1}
	}
	resp, err := client.Put(ctx, client.ServiceURL("images", id, part), body, nil, &gophercloud.RequestOpts{
		MoreHeaders: headers,
		OkCodes:     []int{204},
	})
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err
}

// discardImage deletes an image whose upload failed, even if ctx was cancelled.
func discardImage(ctx context.Context, client *gophercloud.ServiceClient, id string, cause error) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := images.Delete(cctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("%w (deleting the partial image also failed: %w)", cause, err)
	}
	return fmt.Errorf("%w (the partial image was deleted)", cause)
}

// progressReader draws a percentage bar as the body is read. Seek passes
// through so gophercloud can rewind the body on re-authentication.
type progressReader struct {
	r     io.Reader
	total int64
	done  int64
	last  int
	w     io.Writer
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	p.draw()
	return n, err
}

func (p *progressReader) Seek(offset int64, whence int) (int64, error) {
	s, ok := p.r.(io.Seeker)
	if !ok {
		return 0, errors.New("image data is not seekable")
	}
	pos, err := s.Seek(offset, whence)
	if err == nil {
		p.done, p.last = pos, -1
	}
	return pos, err
}

func (p *progressReader) draw() {
	pct := int(min(p.done, p.total) * 100 / p.total)
	if pct == p.last {
		return
	}
	p.last = pct
	const width = 40
	fill := pct * width / 100
	_, _ = fmt.Fprintf(p.w, "\r[%s%s] %3d%%", strings.Repeat("=", fill), strings.Repeat(" ", width-fill), pct)
	if pct == 100 {
		_, _ = fmt.Fprintln(p.w)
	}
}

// --- image create --volume -------------------------------------------------

func createImageFromVolume(ctx context.Context, cmd *cobra.Command, session *auth.Client,
	o *output.Options, name string, f *imageCreateFlags,
) error {
	client, err := session.Volume()
	if err != nil {
		return err
	}
	volumeID, err := resolve.VolumeID(ctx, client, f.volume)
	if err != nil {
		return err
	}
	return runImageCreateFromVolume(ctx, client, o, name, volumeID, f, cmd.OutOrStdout())
}

// runImageCreateFromVolume uploads a volume to glance via cinder's
// os-volume_upload_image action. Visibility and protection need cinder 3.1;
// there they default to private/unprotected, as upstream sends.
func runImageCreateFromVolume(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name, volumeID string, f *imageCreateFlags, w io.Writer,
) error {
	visibility, err := f.resolvedVisibility()
	if err != nil {
		return err
	}
	opts := volumes.UploadImageOpts{
		ImageName:       name,
		Force:           f.force,
		DiskFormat:      cmp.Or(f.diskFormat, defaultDiskFormat),
		ContainerFormat: cmp.Or(f.containerFormat, defaultContainerFormat),
	}
	switch {
	case volumeAtLeast31(client.Microversion):
		opts.Visibility = string(images.ImageVisibilityPrivate)
		if visibility != nil {
			opts.Visibility = string(*visibility)
		}
		opts.Protected = f.protected
	case visibility != nil || f.protected || f.unprotected:
		return errors.New("--os-volume-api-version 3.1 or later is required for the --public, --private, " +
			"--community, --shared, --visibility, --protected or --unprotected option")
	}
	vi, err := volumes.UploadImage(ctx, client, volumeID, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating image %q from volume %s: %w", name, volumeID, err)
	}
	return o.WriteSingle(w,
		[]string{"container_format", "disk_format", "display_description", "id", "image_id", "image_name",
			"protected", "size", "status", "updated_at", "visibility", "volume_type"},
		[]any{vi.ContainerFormat, vi.DiskFormat, vi.Description, vi.VolumeID, vi.ImageID, vi.ImageName,
			vi.Protected, vi.Size, vi.Status, vi.UpdatedAt, vi.Visibility, vi.VolumeType.Name})
}

// volumeAtLeast31 reports whether a cinder microversion ("latest" or "3.N") is 3.1+.
func volumeAtLeast31(mv string) bool {
	if mv == "latest" {
		return true
	}
	major, minor, ok := strings.Cut(mv, ".")
	if !ok || major != "3" {
		return false
	}
	n, err := strconv.Atoi(minor)
	return err == nil && n >= 1
}
