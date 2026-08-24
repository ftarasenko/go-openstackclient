package image

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imageimport"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newImageImportCommand builds "image import ...".
//
// Flag names follow upstream OSC (`openstack image import`). The KeyStack command
// reference at https://docs.keystack.ru/ was not reachable at implementation time
// (HTTP 403), so these are UNVERIFIED against KeyStack and fall back to upstream
// OSC semantics.
//
// The parent doubles as the verb: `image import <image>` starts an import, while
// `image import info` renders the discovery document. Cobra resolves the
// subcommand first, so an image literally named "info" must be given by UUID.
func newImageImportCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &imageImportFlags{}
	cmd := &cobra.Command{
		Use:   "import <image>",
		Short: "Import image data into an existing image record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if err := f.resolveMethod(cmd); err != nil {
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
			return runImageImport(ctx, client, args[0], id, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	// Both spellings exist in the wild: OSC's parser calls this
	// --import-method, while the shorter --method is what operators type (and
	// what turned up in the shell history this command was added for). They are
	// aliases; giving both with different values is an error.
	fl.StringVar(&f.method, flagMethod, "", "import method: web-download or glance-direct (default web-download when --uri is given)")
	fl.StringVar(&f.importMethod, flagImportMethod, "", "alias of --method (upstream OSC spelling)")
	fl.StringVar(&f.uri, "uri", "", "source URL for the web-download method")
	fl.StringSliceVar(&f.stores, "store", nil, "backend store to import into (repeatable)")
	fl.BoolVar(&f.allStores, "all-stores", false, "import into every backend store glance has")
	cmd.MarkFlagsMutuallyExclusive("store", "all-stores")

	cmd.AddCommand(newImageImportInfoCommand(a, o))
	return cmd
}

type imageImportFlags struct {
	method       string
	importMethod string
	uri          string
	stores       []string
	allStores    bool

	// resolved is --method/--import-method reconciled and defaulted by
	// resolveMethod, so the run seam takes a settled method.
	resolved imageimport.ImportMethod
}

// importMethods are the methods glance's Import API defines. The endpoint
// advertises which of them it actually enables — see `image import info`.
var importMethods = []string{
	string(imageimport.WebDownloadMethod),
	string(imageimport.GlanceDirectMethod),
}

// resolveMethod reconciles --method / --import-method and applies the
// web-download default, which is the only method that works without first
// staging data (koc has no `image stage`).
func (f *imageImportFlags) resolveMethod(cmd *cobra.Command) error {
	fl := cmd.Flags()
	switch {
	case fl.Changed(flagMethod) && fl.Changed(flagImportMethod):
		if f.method != f.importMethod {
			return fmt.Errorf("--method and --import-method are aliases but were given different values (%q and %q)", f.method, f.importMethod)
		}
	case fl.Changed(flagImportMethod):
		f.method = f.importMethod
	}

	if f.method == "" {
		if f.uri == "" {
			return fmt.Errorf("no import method given: pass --method (%s), or --uri to default to web-download", strings.Join(importMethods, " or "))
		}
		f.method = string(imageimport.WebDownloadMethod)
	}
	if !slices.Contains(importMethods, f.method) {
		return fmt.Errorf("unsupported import method %q: expected one of %s", f.method, strings.Join(importMethods, ", "))
	}
	if f.method == string(imageimport.WebDownloadMethod) && f.uri == "" {
		return fmt.Errorf("--uri is required for the web-download import method")
	}
	if f.method == string(imageimport.GlanceDirectMethod) && f.uri != "" {
		return fmt.Errorf("--uri applies to the web-download method only; glance-direct imports data staged beforehand")
	}
	f.resolved = imageimport.ImportMethod(f.method)
	return nil
}

// importStoreOpts adds glance's multi-store keys to an import request.
// gophercloud's imageimport.CreateOpts only carries the `method` object, but
// `stores` / `all_stores` sit next to it at the *top* level of the body (see
// openstacksdk `image/v2/image.py import_image`), so the builder interface is
// implemented here rather than reaching for a raw Post: everything else about
// the call — URL, 202 OkCodes, error wrapping — stays gophercloud's.
//
// Both keys are omitted unless asked for, keeping the default request
// byte-identical to one built from CreateOpts alone. Upstream OSC always sends
// `all_stores: false`, which is the same request with an extra key.
type importStoreOpts struct {
	imageimport.CreateOpts
	Stores    []string
	AllStores bool
}

func (o importStoreOpts) ToImportCreateMap() (map[string]any, error) {
	b, err := o.CreateOpts.ToImportCreateMap()
	if err != nil {
		return nil, err
	}
	if o.AllStores {
		b["all_stores"] = true
	}
	if len(o.Stores) > 0 {
		b["stores"] = o.Stores
	}
	return b, nil
}

// runImageImport kicks off the import. Glance returns 202 with no body: the
// import runs asynchronously, so progress is observed with `image show`
// (status goes importing → active) rather than reported here.
func runImageImport(ctx context.Context, client *gophercloud.ServiceClient, ref, id string,
	f *imageImportFlags, w io.Writer,
) error {
	opts := importStoreOpts{
		CreateOpts: imageimport.CreateOpts{Name: f.resolved, URI: f.uri},
		Stores:     f.stores,
		AllStores:  f.allStores,
	}
	if err := imageimport.Create(ctx, client, id, opts).ExtractErr(); err != nil {
		return fmt.Errorf("importing image %q: %w", ref, err)
	}
	if _, err := fmt.Fprintf(w,
		"Started %s import of image %s; poll \"koc image show %s\" until status is active\n",
		f.resolved, ref, ref); err != nil {
		return err
	}
	return nil
}

func newImageImportInfoCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show the import methods this glance endpoint enables",
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
			return runImageImportInfo(ctx, client, o, cmd.OutOrStdout())
		},
	}
}

func runImageImportInfo(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, w io.Writer) error {
	info, err := imageimport.Get(ctx, client).Extract()
	if err != nil {
		return fmt.Errorf("getting image import info: %w", err)
	}
	return o.WriteSingle(w,
		[]string{"import-methods", "description", "type"},
		[]any{info.ImportMethods.Value, info.ImportMethods.Description, info.ImportMethods.Type})
}
