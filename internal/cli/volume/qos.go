package volume

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/qos"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/paging"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// "volume qos" — cinder QoS specs and their association with volume types.
//
// Flag names follow upstream OSC (`openstack volume qos ...`). UNVERIFIED
// against KeyStack docs (https://docs.keystack.ru/ returned HTTP 403 at
// implementation time); falls back to upstream OSC semantics.

func newQoSCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "qos", Short: "Manage volume QoS specifications"}
	cmd.AddCommand(
		newQoSListCommand(a, o),
		newQoSShowCommand(a, o),
		newQoSCreateCommand(a, o),
		newQoSDeleteCommand(a, o),
		newQoSSetCommand(a, o),
		newQoSUnsetCommand(a, o),
		newQoSAssociateCommand(a, o),
		newQoSDisassociateCommand(a, o),
	)
	return cmd
}

func qosShowFields(q *qos.QoS) ([]string, []any) {
	return []string{"id", "name", "consumer", "properties"},
		[]any{q.ID, q.Name, q.Consumer, q.Specs}
}

// --- list / show ------------------------------------------------------------

func newQoSListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List volume QoS specifications",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runQoSList(ctx, client, o, limit, cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum number of QoS specifications to return")
	return cmd
}

func runQoSList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, limit int, w io.Writer) error {
	all, err := paging.Collect(ctx, qos.List(client, qos.ListOpts{Limit: limit}), limit, qos.ExtractQoS)
	if err != nil {
		return fmt.Errorf("listing volume QoS specifications: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Consumer", "Properties"}, Rows: make([][]any, 0, len(all))}
	for _, q := range all {
		t.Rows = append(t.Rows, []any{q.ID, q.Name, q.Consumer, q.Specs})
	}
	return o.WriteList(w, t)
}

func newQoSShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <qos-spec>",
		Short: "Show a volume QoS specification",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runQoSShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runQoSShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := resolveQoSID(ctx, client, ref)
	if err != nil {
		return err
	}
	q, err := qos.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing volume QoS specification %q: %w", ref, err)
	}
	fields, values := qosShowFields(q)
	return o.WriteSingle(w, fields, values)
}

// resolveQoSID passes a UUID through and otherwise matches on name. Cinder has
// no name filter on this listing, so the match is client-side.
func resolveQoSID(ctx context.Context, client *gophercloud.ServiceClient, ref string) (string, error) {
	if resolve.IsUUID(ref) {
		return ref, nil
	}
	pages, err := qos.List(client, qos.ListOpts{}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("looking up QoS specification %q: %w", ref, err)
	}
	all, err := qos.ExtractQoS(pages)
	if err != nil {
		return "", fmt.Errorf("parsing the QoS specification list: %w", err)
	}
	var matches []string
	for _, q := range all {
		if q.Name == ref {
			matches = append(matches, q.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return ref, nil
	default:
		return "", fmt.Errorf("QoS specification %q is ambiguous: %d specs share that name; use an ID", ref, len(matches))
	}
}

// --- create / delete --------------------------------------------------------

func newQoSCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var consumer string
	var properties []string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a volume QoS specification",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runQoSCreate(ctx, client, o, args[0], consumer, properties, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&consumer, "consumer", "", "who applies the QoS: front-end, back-end or both (cinder default: both)")
	fl.StringArrayVar(&properties, "property", nil, "QoS property key=value (repeatable)")
	return cmd
}

func runQoSCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	name, consumer string, properties []string, w io.Writer,
) error {
	specs, err := parseProperties(properties)
	if err != nil {
		return fmt.Errorf("parsing --property: %w", err)
	}
	q, err := qos.Create(ctx, client, qos.CreateOpts{
		Name:     name,
		Consumer: qos.QoSConsumer(consumer),
		Specs:    specs,
	}).Extract()
	if err != nil {
		return fmt.Errorf("creating volume QoS specification %q: %w", name, err)
	}
	fields, values := qosShowFields(q)
	return o.WriteSingle(w, fields, values)
}

func newQoSDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <qos-spec> [<qos-spec> ...]",
		Short: "Delete volume QoS specification(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runQoSDelete(ctx, client, args, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "delete even while volume types are still associated")
	return cmd
}

func runQoSDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, force bool) error {
	return batchdelete.Each(refs, func(ref string) error {
		id, err := resolveQoSID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := qos.Delete(ctx, client, id, qos.DeleteOpts{Force: force}).ExtractErr(); err != nil {
			return fmt.Errorf("deleting volume QoS specification %q: %w", ref, err)
		}
		return nil
	})
}

// --- set / unset ------------------------------------------------------------

func newQoSSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var properties []string
	var noProperty bool
	cmd := &cobra.Command{
		Use:   "set <qos-spec>",
		Short: "Set properties on a volume QoS specification",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if len(properties) == 0 && !noProperty {
				return fmt.Errorf("volume qos set requires --property or --no-property")
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runQoSSet(ctx, client, o, args[0], properties, noProperty, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&properties, "property", nil, "QoS property key=value (repeatable)")
	fl.BoolVar(&noProperty, "no-property", false, "remove every existing property before applying --property")
	return cmd
}

// runQoSSet updates the spec. Cinder's PUT merges keys rather than replacing
// the map, so --no-property has to delete the existing keys explicitly first —
// otherwise it would be a no-op.
func runQoSSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, properties []string, noProperty bool, w io.Writer,
) error {
	id, err := resolveQoSID(ctx, client, ref)
	if err != nil {
		return err
	}
	add, err := parseProperties(properties)
	if err != nil {
		return fmt.Errorf("parsing --property: %w", err)
	}
	if noProperty {
		current, gerr := qos.Get(ctx, client, id).Extract()
		if gerr != nil {
			return fmt.Errorf("reading QoS specification %q before clearing it: %w", ref, gerr)
		}
		var keys []string
		for k := range current.Specs {
			if _, keep := add[k]; !keep {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			if derr := qos.DeleteKeys(ctx, client, id, qos.DeleteKeysOpts(keys)).ExtractErr(); derr != nil {
				return fmt.Errorf("clearing properties of QoS specification %q: %w", ref, derr)
			}
		}
	}
	if len(add) > 0 {
		if _, err := qos.Update(ctx, client, id, qos.UpdateOpts{Specs: add}).Extract(); err != nil {
			return fmt.Errorf("updating QoS specification %q: %w", ref, err)
		}
	}
	return runQoSShow(ctx, client, o, id, w)
}

func newQoSUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var properties []string
	cmd := &cobra.Command{
		Use:   "unset <qos-spec>",
		Short: "Remove properties from a volume QoS specification",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			if len(properties) == 0 {
				return fmt.Errorf("volume qos unset requires at least one --property")
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runQoSUnset(ctx, client, o, args[0], properties, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringArrayVar(&properties, "property", nil, "property key to remove (repeatable)")
	return cmd
}

func runQoSUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	ref string, keys []string, w io.Writer,
) error {
	id, err := resolveQoSID(ctx, client, ref)
	if err != nil {
		return err
	}
	if err := qos.DeleteKeys(ctx, client, id, qos.DeleteKeysOpts(keys)).ExtractErr(); err != nil {
		return fmt.Errorf("removing properties from QoS specification %q: %w", ref, err)
	}
	return runQoSShow(ctx, client, o, id, w)
}

// --- associate / disassociate -----------------------------------------------

func newQoSAssociateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "associate <qos-spec> <volume-type>",
		Short: "Associate a QoS specification with a volume type",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runQoSAssociate(ctx, client, args[0], args[1])
		},
	}
}

func runQoSAssociate(ctx context.Context, client *gophercloud.ServiceClient, qosRef, typeRef string) error {
	qosID, err := resolveQoSID(ctx, client, qosRef)
	if err != nil {
		return err
	}
	typeID, err := resolveVolumeTypeID(ctx, client, typeRef)
	if err != nil {
		return err
	}
	if err := qos.Associate(ctx, client, qosID, qos.AssociateOpts{VolumeTypeID: typeID}).ExtractErr(); err != nil {
		return fmt.Errorf("associating QoS specification %q with volume type %q: %w", qosRef, typeRef, err)
	}
	return nil
}

func newQoSDisassociateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var volumeType string
	var all bool
	cmd := &cobra.Command{
		Use:   "disassociate <qos-spec>",
		Short: "Disassociate a QoS specification from a volume type",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runQoSDisassociate(ctx, client, args[0], volumeType, all)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&volumeType, flagVolumeType, "", "volume type to disassociate (name or ID)")
	fl.BoolVar(&all, flagAll, false, "disassociate every volume type")
	cmd.MarkFlagsMutuallyExclusive(flagVolumeType, flagAll)
	cmd.MarkFlagsOneRequired(flagVolumeType, flagAll)
	return cmd
}

func runQoSDisassociate(ctx context.Context, client *gophercloud.ServiceClient, qosRef, typeRef string, all bool) error {
	qosID, err := resolveQoSID(ctx, client, qosRef)
	if err != nil {
		return err
	}
	if all {
		if err := qos.DisassociateAll(ctx, client, qosID).ExtractErr(); err != nil {
			return fmt.Errorf("disassociating every volume type from QoS specification %q: %w", qosRef, err)
		}
		return nil
	}
	typeID, err := resolveVolumeTypeID(ctx, client, typeRef)
	if err != nil {
		return err
	}
	if err := qos.Disassociate(ctx, client, qosID, qos.DisassociateOpts{VolumeTypeID: typeID}).ExtractErr(); err != nil {
		return fmt.Errorf("disassociating volume type %q from QoS specification %q: %w", typeRef, qosRef, err)
	}
	return nil
}
