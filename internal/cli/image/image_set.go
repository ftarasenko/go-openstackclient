package image

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// imageSetFlags holds the mutable attributes accepted by "image set".
//
// Flag names follow upstream OSC (`openstack image set`). UNVERIFIED against the
// KeyStack reference (docs.keystack.ru returned HTTP 403 at implementation
// time); falls back to upstream OSC semantics.
type imageSetFlags struct {
	name        string
	property    []string
	minDisk     int
	minDiskSet  bool
	minRAM      int
	minRAMSet   bool
	public      bool
	private     bool
	protected   bool
	unprotected bool
	activate    bool
	deactivate  bool
}

func newImageSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &imageSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <image>",
		Short: "Set image properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.minDiskSet = cmd.Flags().Changed("min-disk")
			f.minRAMSet = cmd.Flags().Changed("min-ram")
			ctx := cmd.Context()
			client, err := newImageClient(ctx, a)
			if err != nil {
				return err
			}
			id, err := resolveImageID(ctx, client, args[0])
			if err != nil {
				return err
			}
			return runImageSet(ctx, client, o, id, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "set the image name")
	fl.StringArrayVar(&f.property, "property", nil, "set an image property key=value (repeatable)")
	fl.IntVar(&f.minDisk, "min-disk", 0, "set the minimum disk size in GB")
	fl.IntVar(&f.minRAM, "min-ram", 0, "set the minimum RAM in MB")
	fl.BoolVar(&f.public, "public", false, "make the image public")
	fl.BoolVar(&f.private, "private", false, "make the image private")
	fl.BoolVar(&f.protected, "protected", false, "prevent the image from being deleted")
	fl.BoolVar(&f.unprotected, "unprotected", false, "allow the image to be deleted")
	fl.BoolVar(&f.activate, "activate", false, "activate (reactivate) the image")
	fl.BoolVar(&f.deactivate, "deactivate", false, "deactivate the image")
	cmd.MarkFlagsMutuallyExclusive("public", "private")
	cmd.MarkFlagsMutuallyExclusive("protected", "unprotected")
	cmd.MarkFlagsMutuallyExclusive("activate", "deactivate")
	return cmd
}

func runImageSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, f *imageSetFlags, w io.Writer) error {
	var ops images.UpdateOpts
	if f.name != "" {
		ops = append(ops, images.ReplaceImageName{NewName: f.name})
	}
	if f.minDiskSet {
		ops = append(ops, images.ReplaceImageMinDisk{NewMinDisk: f.minDisk})
	}
	if f.minRAMSet {
		ops = append(ops, images.ReplaceImageMinRam{NewMinRam: f.minRAM})
	}
	if f.public {
		ops = append(ops, images.UpdateVisibility{Visibility: images.ImageVisibilityPublic})
	} else if f.private {
		ops = append(ops, images.UpdateVisibility{Visibility: images.ImageVisibilityPrivate})
	}
	if f.protected {
		ops = append(ops, images.ReplaceImageProtected{NewProtected: true})
	} else if f.unprotected {
		ops = append(ops, images.ReplaceImageProtected{NewProtected: false})
	}
	for _, p := range f.property {
		k, v, err := parseKeyVal(p)
		if err != nil {
			return fmt.Errorf("parsing --property: %w", err)
		}
		// "add" doubles as "replace" for a scalar path in the glance JSON patch.
		// gophercloud builds the patch path as "/<Name>" without RFC 6901
		// escaping, so escape the key here to keep the JSON pointer valid when it
		// contains '/' or '~'.
		ops = append(ops, images.UpdateImageProperty{Op: images.AddOp, Name: escapeJSONPointer(k), Value: v})
	}

	// activate/deactivate use dedicated glance action endpoints; there is no typed
	// gophercloud verb, so they are issued via the raw client (see setImageActive).
	if f.activate || f.deactivate {
		if err := setImageActive(ctx, client, id, f.activate); err != nil {
			return err
		}
	}

	if len(ops) == 0 {
		if f.activate || f.deactivate {
			// Only an activation change was requested; report current state.
			return runImageShow(ctx, client, o, id, w)
		}
		return fmt.Errorf("image set requires at least one attribute flag")
	}

	img, err := images.Update(ctx, client, id, ops).Extract()
	if err != nil {
		return fmt.Errorf("updating image %s: %w", id, err)
	}
	fields, values := imageShowFields(img)
	return o.WriteSingle(w, fields, values)
}

// setImageActive issues the glance action to (de)activate an image. glance
// exposes POST /v2/images/{id}/actions/{reactivate|deactivate} (204). gophercloud
// v2.13.0 has no typed verb for this, so the request is built on the raw client.
func setImageActive(ctx context.Context, client *gophercloud.ServiceClient, id string, activate bool) error {
	action := "deactivate"
	if activate {
		action = "reactivate"
	}
	url := client.ServiceURL("images", id, "actions", action)
	resp, err := client.Post(ctx, url, nil, nil, &gophercloud.RequestOpts{OkCodes: []int{204}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	if err != nil {
		return fmt.Errorf("%s image %s: %w", action, id, err)
	}
	return nil
}

// imageUnsetFlags holds the attributes removable by "image unset".
type imageUnsetFlags struct {
	property []string
}

func newImageUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &imageUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <image>",
		Short: "Unset image properties",
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
			return runImageUnset(ctx, client, o, id, f, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&f.property, "property", nil, "remove an image property by key (repeatable)")
	return cmd
}

func runImageUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, f *imageUnsetFlags, w io.Writer) error {
	if len(f.property) == 0 {
		return fmt.Errorf("image unset requires at least one --property")
	}
	var ops images.UpdateOpts
	for _, k := range f.property {
		// Escape the key per RFC 6901 so a '/' or '~' produces a valid pointer
		// (gophercloud builds the path as "/<Name>" without escaping).
		ops = append(ops, images.UpdateImageProperty{Op: images.RemoveOp, Name: escapeJSONPointer(k)})
	}
	img, err := images.Update(ctx, client, id, ops).Extract()
	if err != nil {
		return fmt.Errorf("updating image %s: %w", id, err)
	}
	fields, values := imageShowFields(img)
	return o.WriteSingle(w, fields, values)
}
