package volume

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// newVolumeExtendCommand builds "volume extend <volume> <size>", the cinder CLI's
// spelling of what koc otherwise expresses as "volume set --size". It is a
// distinct upstream command (`openstack volume extend`), not just an alias, so it
// gets its own entry rather than a cobra Aliases entry on "set" — the size is a
// positional argument there, not a flag.
func newVolumeExtendCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "extend <volume> <new-size-in-GiB>",
		Short: "Extend a volume to a larger size",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			size, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("size %q is not a number of GiB", args[1])
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runVolumeExtend(ctx, client, o, args[0], size, cmd.OutOrStdout())
		},
	}
}

func runVolumeExtend(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, size int, w io.Writer,
) error {
	if size <= 0 {
		return fmt.Errorf("new size must be a positive number of GiB, got %d", size)
	}
	id, err := resolveVolumeID(ctx, client, ref)
	if err != nil {
		return err
	}
	if err := volumes.ExtendSize(ctx, client, id, volumes.ExtendSizeOpts{NewSize: size}).ExtractErr(); err != nil {
		return fmt.Errorf("extending volume %q to %d GiB: %w", ref, size, err)
	}
	// Cinder answers 202 with no body and resizes asynchronously, so the record is
	// re-fetched to report the status the extend left the volume in.
	v, err := volumes.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting volume %q after extend: %w", ref, err)
	}
	fields, values := volumeShowFields(v)
	return o.WriteSingle(w, fields, values)
}
