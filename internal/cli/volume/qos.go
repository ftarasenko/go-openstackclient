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
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
	volumeapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/volume"
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
	all, err := volumeapi.ListQoS(ctx, client, qos.ListOpts{Limit: limit}, limit)
	if err != nil {
		return err
	}
	// Associations costs one request per spec, as upstream's does, so it is
	// only paid for when the column is going to be rendered.
	wantAssoc := len(o.Columns) == 0 || len(o.SelectedColumns("Associations")) > 0
	t := output.Table{Columns: []string{"ID", "Name", "Consumer", "Associations", "Properties"}, Rows: make([][]any, 0, len(all))}
	for _, q := range all {
		assoc := []string{}
		if wantAssoc {
			if assoc, err = qosAssociationNames(ctx, client, q.ID); err != nil {
				return err
			}
		}
		t.Rows = append(t.Rows, []any{q.ID, q.Name, q.Consumer, assoc, q.Specs})
	}
	return o.WriteList(w, t)
}

// qosAssociationNames returns the names of the volume types a QoS spec is
// associated with — upstream's Associations column (python-openstackclient
// 10.3.0, volume/v3/qos_specs.py ListQos), which it reads from the spec's
// associations endpoint, one request per spec.
func qosAssociationNames(ctx context.Context, client *gophercloud.ServiceClient, id string) ([]string, error) {
	all, err := volumeapi.ListQoSAssociations(ctx, client, id)
	if err != nil {
		return nil, relabel(err, "listing the associations of volume QoS specification %q", id)
	}
	names := make([]string, 0, len(all))
	for _, a := range all {
		names = append(names, a.Name)
	}
	return names, nil
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
	q, err := volumeapi.GetQoS(ctx, client, id)
	if err != nil {
		return relabel(err, "showing volume QoS specification %q", ref)
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
	all, err := volumeapi.ListQoS(ctx, client, qos.ListOpts{}, 0)
	if err != nil {
		return "", relabel(err, "looking up QoS specification %q", ref)
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
	if err := o.CheckColumns(qosColumns...); err != nil {
		return err
	}
	specs, err := parseProperties(properties)
	if err != nil {
		return fmt.Errorf("parsing --property: %w", err)
	}
	q, err := volumeapi.CreateQoS(ctx, client, qos.CreateOpts{
		Name:     name,
		Consumer: qos.QoSConsumer(consumer),
		Specs:    specs,
	})
	if err != nil {
		return err
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
		if err := volumeapi.DeleteQoS(ctx, client, id, force); err != nil {
			return relabel(err, "deleting volume QoS specification %q", ref)
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
		if err := clearOtherProperties(ctx, client, id, ref, add); err != nil {
			return err
		}
	}
	if len(add) > 0 {
		if _, err := volumeapi.SetQoSSpecs(ctx, client, id, add); err != nil {
			return relabel(err, "updating QoS specification %q", ref)
		}
	}
	return runQoSShow(ctx, client, o, id, w)
}

// clearOtherProperties implements --no-property: it reads the specification's
// current properties and deletes the ones --property is not about to set.
//
// Cinder's PUT merges keys rather than replacing the map, so the existing keys
// have to be deleted explicitly or --no-property would be a no-op. The keys that
// are about to be re-set are excluded from the delete so they are never briefly
// absent, and so a delete-then-set of the same key cannot leave the
// specification empty if the second call fails.
func clearOtherProperties(ctx context.Context, client *gophercloud.ServiceClient,
	id, ref string, keep map[string]string,
) error {
	current, err := volumeapi.GetQoS(ctx, client, id)
	if err != nil {
		return relabel(err, "reading QoS specification %q before clearing it", ref)
	}
	keys := staleProperties(current.Specs, keep)
	if len(keys) == 0 {
		return nil
	}
	if err := volumeapi.DeleteQoSKeys(ctx, client, id, keys); err != nil {
		return relabel(err, "clearing properties of QoS specification %q", ref)
	}
	return nil
}

// staleProperties returns the keys of current that keep does not re-set — the
// survivors of a --no-property clear, inverted into the delete list cinder
// takes.
func staleProperties(current, keep map[string]string) []string {
	var keys []string
	for k := range current {
		if _, kept := keep[k]; !kept {
			keys = append(keys, k)
		}
	}
	return keys
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
	if err := volumeapi.DeleteQoSKeys(ctx, client, id, keys); err != nil {
		return relabel(err, "removing properties from QoS specification %q", ref)
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
	if err := volumeapi.AssociateQoS(ctx, client, qosID, typeID); err != nil {
		return relabel(err, "associating QoS specification %q with volume type %q", qosRef, typeRef)
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
		if err := volumeapi.DisassociateAllQoS(ctx, client, qosID); err != nil {
			return relabel(err, "disassociating every volume type from QoS specification %q", qosRef)
		}
		return nil
	}
	typeID, err := resolveVolumeTypeID(ctx, client, typeRef)
	if err != nil {
		return err
	}
	if err := volumeapi.DisassociateQoS(ctx, client, qosID, typeID); err != nil {
		return relabel(err, "disassociating volume type %q from QoS specification %q", typeRef, qosRef)
	}
	return nil
}
