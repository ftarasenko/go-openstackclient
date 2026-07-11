package image

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imagedata"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// imageShowFields is the curated Field/Value view for a single image, matching
// the most operationally useful attributes shown by `openstack image show`.
func imageShowFields(img *images.Image) ([]string, []any) {
	fields := []string{
		"id", "name", "status", "visibility", "protected", "hidden",
		"size", "virtual_size", "disk_format", "container_format",
		"min_disk", "min_ram", "owner", "checksum", "tags", "properties",
		"file", "schema", "created_at", "updated_at",
	}
	values := []any{
		img.ID, img.Name, string(img.Status), string(img.Visibility), img.Protected, img.Hidden,
		img.SizeBytes, img.VirtualSize, img.DiskFormat, img.ContainerFormat,
		img.MinDiskGigabytes, img.MinRAMMegabytes, img.Owner, img.Checksum, img.Tags, img.Properties,
		img.File, img.Schema, img.CreatedAt, img.UpdatedAt,
	}
	return fields, values
}

// imageListFlags holds the filters accepted by "image list".
//
// Flag names follow upstream OSC (`openstack image list`). The KeyStack command
// reference at https://docs.keystack.ru/ was not reachable at implementation
// time (HTTP 403), so these are UNVERIFIED against KeyStack and fall back to
// upstream OSC semantics.
type imageListFlags struct {
	long      bool
	name      string
	public    bool
	private   bool
	shared    bool
	community bool
}

func (f *imageListFlags) visibility() images.ImageVisibility {
	switch {
	case f.public:
		return images.ImageVisibilityPublic
	case f.private:
		return images.ImageVisibilityPrivate
	case f.shared:
		return images.ImageVisibilityShared
	case f.community:
		return images.ImageVisibilityCommunity
	default:
		return ""
	}
}

func newImageListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &imageListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newImageClient(ctx, a)
			if err != nil {
				return err
			}
			return runImageList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.StringVar(&f.name, "name", "", "filter images by name")
	fl.BoolVar(&f.public, "public", false, "list only public images")
	fl.BoolVar(&f.private, "private", false, "list only private images")
	fl.BoolVar(&f.shared, "shared", false, "list only shared images")
	fl.BoolVar(&f.community, "community", false, "list only community images")
	cmd.MarkFlagsMutuallyExclusive("public", "private", "shared", "community")
	return cmd
}

// runImageList performs the list and renders it. It takes an already-built
// service client so it can be exercised directly against a mock endpoint.
func runImageList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *imageListFlags, w io.Writer) error {
	opts := images.ListOpts{
		Name:       f.name,
		Visibility: f.visibility(),
	}
	pages, err := images.List(client, opts).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing images: %w", err)
	}
	all, err := images.ExtractImages(pages)
	if err != nil {
		return fmt.Errorf("parsing image list: %w", err)
	}
	return o.WriteList(w, imageListTable(all, f.long))
}

// imageListTable builds the output table. The default column set matches
// `openstack image list`; --long adds the operationally useful extras.
func imageListTable(list []images.Image, long bool) output.Table {
	cols := []string{"ID", "Name", "Status"}
	if long {
		cols = append(cols, "Visibility", "Protected", "Disk Format", "Container Format", "Size", "Owner")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(list))}
	for _, img := range list {
		row := []any{img.ID, img.Name, string(img.Status)}
		if long {
			row = append(row, string(img.Visibility), img.Protected, img.DiskFormat, img.ContainerFormat, img.SizeBytes, img.Owner)
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// newImageShowCommand builds "image show <image>".
func newImageShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <image>",
		Short: "Show details of an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newImageClient(ctx, a)
			if err != nil {
				return err
			}
			id, err := resolveImageID(ctx, client, args[0])
			if err != nil {
				return err
			}
			return runImageShow(ctx, client, o, id, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runImageShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	img, err := images.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting image %s: %w", id, err)
	}
	fields, values := imageShowFields(img)
	return o.WriteSingle(w, fields, values)
}

// imageCreateFlags holds the attributes accepted by "image create".
//
// Flag names follow upstream OSC (`openstack image create`). UNVERIFIED against
// the KeyStack reference (docs.keystack.ru returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.
type imageCreateFlags struct {
	diskFormat      string
	containerFormat string
	file            string
	minDisk         int
	minRAM          int
	public          bool
	private         bool
	property        []string
}

func newImageCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &imageCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create/upload an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newImageClient(ctx, a)
			if err != nil {
				return err
			}
			return runImageCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.diskFormat, "disk-format", "", "disk format: ami, ari, aki, vhd, vmdk, raw, qcow2, vdi, iso")
	fl.StringVar(&f.containerFormat, "container-format", "", "container format: ami, ari, aki, bare, ovf")
	fl.StringVar(&f.file, "file", "", "local file whose contents are uploaded as the image data")
	fl.IntVar(&f.minDisk, "min-disk", 0, "minimum disk size in GB required to boot the image")
	fl.IntVar(&f.minRAM, "min-ram", 0, "minimum RAM in MB required to boot the image")
	fl.BoolVar(&f.public, "public", false, "make the image public")
	fl.BoolVar(&f.private, "private", false, "make the image private")
	fl.StringArrayVar(&f.property, "property", nil, "arbitrary image property key=value (repeatable)")
	cmd.MarkFlagsMutuallyExclusive("public", "private")
	return cmd
}

func runImageCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *imageCreateFlags, w io.Writer) error {
	props, err := parseKeyValMap(f.property)
	if err != nil {
		return fmt.Errorf("parsing --property: %w", err)
	}
	opts := images.CreateOpts{
		Name:            name,
		DiskFormat:      f.diskFormat,
		ContainerFormat: f.containerFormat,
		MinDisk:         f.minDisk,
		MinRAM:          f.minRAM,
		Properties:      props,
	}
	if f.public {
		v := images.ImageVisibilityPublic
		opts.Visibility = &v
	} else if f.private {
		v := images.ImageVisibilityPrivate
		opts.Visibility = &v
	}

	img, err := images.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating image %q: %w", name, err)
	}

	// Stream the file contents as the image data, if provided, then re-fetch so
	// the shown record reflects the resulting status/size/checksum.
	if f.file != "" {
		file, err := os.Open(f.file)
		if err != nil {
			return fmt.Errorf("opening image file %q: %w", f.file, err)
		}
		defer func() { _ = file.Close() }()
		if err := imagedata.Upload(ctx, client, img.ID, file).ExtractErr(); err != nil {
			return fmt.Errorf("uploading image data for %s: %w", img.ID, err)
		}
		img, err = images.Get(ctx, client, img.ID).Extract()
		if err != nil {
			return fmt.Errorf("getting image %s after upload: %w", img.ID, err)
		}
	}

	fields, values := imageShowFields(img)
	return o.WriteSingle(w, fields, values)
}

func newImageDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <image> [<image> ...]",
		Short: "Delete image(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newImageClient(ctx, a)
			if err != nil {
				return err
			}
			ids := make([]string, 0, len(args))
			for _, ref := range args {
				id, err := resolveImageID(ctx, client, ref)
				if err != nil {
					return err
				}
				ids = append(ids, id)
			}
			return runImageDelete(ctx, client, ids, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runImageDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string, w io.Writer) error {
	for _, id := range ids {
		if err := images.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting image %s: %w", id, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted image %s\n", id); err != nil {
			return err
		}
	}
	return nil
}
