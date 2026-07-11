package image

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imagedata"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// imageSaveFlags holds the options accepted by "image save".
type imageSaveFlags struct {
	file string
}

func newImageSaveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &imageSaveFlags{}
	cmd := &cobra.Command{
		Use:   "save <image>",
		Short: "Download image data to a file or stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// "image save" emits raw binary rather than a formatted table, but we
			// still validate the (unused) format flag for consistency.
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
			return runImageSave(ctx, client, id, f, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&f.file, "file", "", "write image data to this path (default: stdout)")
	return cmd
}

// runImageSave downloads the image data and streams it to --file or, when unset,
// to w (stdout). w is the seam for testability.
func runImageSave(ctx context.Context, client *gophercloud.ServiceClient, id string, f *imageSaveFlags, w io.Writer) (err error) {
	body, err := imagedata.Download(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("downloading image %s: %w", id, err)
	}
	defer func() { _ = body.Close() }()

	dst := w
	if f.file != "" {
		out, cerr := os.Create(f.file)
		if cerr != nil {
			return fmt.Errorf("creating output file %q: %w", f.file, cerr)
		}
		defer func() {
			if closeErr := out.Close(); closeErr != nil && err == nil {
				err = fmt.Errorf("closing output file %q: %w", f.file, closeErr)
			}
		}()
		dst = out
	}
	if _, cerr := io.Copy(dst, body); cerr != nil {
		return fmt.Errorf("writing image data: %w", cerr)
	}
	return nil
}
