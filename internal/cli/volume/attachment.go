package volume

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/attachments"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/allprojects"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/cli/paging"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Microversions the cinder attachments API needs. koc negotiates "latest" by
// default, so these only bite when --os-volume-api-version pins an older version.
const (
	// attachmentsMicroversion gates the /attachments resource itself.
	attachmentsMicroversion = "3.27"
	// completeMicroversion gates the os-complete action.
	completeMicroversion = "3.44"
	// attachModeMicroversion gates the top-level "mode" key on create; from 3.27
	// to 3.53 the mode rides in the connector instead.
	attachModeMicroversion = "3.54"
)

// newAttachmentCommand builds "volume attachment ...", the cinder attachments
// API (microversion 3.27+) that models an attachment as a first-class resource:
// reserve (create without a connector) → update with connector info → complete.
//
// Flag names follow upstream OSC (`openstack volume attachment ...`); the
// KeyStack reference (docs.keystack.ru) returned HTTP 403 at implementation time,
// so the surface is UNVERIFIED against KeyStack and falls back to upstream OSC.
func newAttachmentCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attachment",
		Short: "Manage volume attachments (cinder attachments API, 3.27+)",
	}
	cmd.AddCommand(newAttachmentListCommand(a, o))
	cmd.AddCommand(newAttachmentShowCommand(a, o))
	cmd.AddCommand(newAttachmentCreateCommand(a, o))
	cmd.AddCommand(newAttachmentDeleteCommand(a, o))
	cmd.AddCommand(newAttachmentSetCommand(a, o))
	cmd.AddCommand(newAttachmentCompleteCommand(a, o))
	return cmd
}

// attachmentShowFields is the curated Field/Value view for a single attachment.
func attachmentShowFields(at *attachments.Attachment) ([]string, []any) {
	fields := []string{
		"id", "volume_id", "instance", "status", "attach_mode",
		"attached_at", "detached_at", "connection_info",
	}
	values := []any{
		at.ID, at.VolumeID, at.Instance, at.Status, at.AttachMode,
		formatAttachTime(at.AttachedAt), formatAttachTime(at.DetachedAt), at.ConnectionInfo,
	}
	return fields, values
}

// formatAttachTime renders an attach timestamp, leaving the cell empty rather
// than printing Go's zero time when cinder sent null (a live attachment has no
// detached_at, and a reserved one has no attached_at yet).
func formatAttachTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// attachmentListFlags holds the filters accepted by "volume attachment list".
type attachmentListFlags struct {
	allProjects bool
	project     string
	volume      string
	status      string
	limit       int
	marker      string
}

func newAttachmentListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &attachmentListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List volume attachments",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newVolumeSession(ctx, a)
			if err != nil {
				return err
			}
			// --project names a keystone project, so resolve it via identity.
			if f.project != "" && !resolve.IsUUID(f.project) {
				identity, err := session.Identity()
				if err != nil {
					return err
				}
				id, err := resolve.ProjectID(ctx, identity, f.project)
				if err != nil {
					return err
				}
				f.project = id
			}
			return runAttachmentList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	allprojects.Bind(cmd, &f.allProjects, "list attachments from all projects (admin)")
	fl.StringVar(&f.project, "project", "", "filter by project (ID or name; admin)")
	fl.StringVar(&f.volume, "volume-id", "", "filter by volume (ID or name)")
	fl.StringVar(&f.status, "status", "", "filter by attachment status")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of attachments to return")
	fl.StringVar(&f.marker, "marker", "", "list attachments after this ID (pagination)")
	return cmd
}

func runAttachmentList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *attachmentListFlags, w io.Writer) error {
	if err := requireVolumeMicroversion(client, attachmentsMicroversion, "volume attachment list"); err != nil {
		return err
	}
	// The filter takes a volume ID; accept a name too, as every other volume
	// command does.
	volumeID := f.volume
	if volumeID != "" {
		var err error
		if volumeID, err = resolveVolumeID(ctx, client, volumeID); err != nil {
			return err
		}
	}
	opts := attachments.ListOpts{
		AllTenants: f.allProjects,
		ProjectID:  f.project,
		VolumeID:   volumeID,
		Status:     f.status,
		Limit:      f.limit,
		Marker:     f.marker,
	}
	// Limit is only the page size to cinder; enforce it as a hard result cap.
	all, err := paging.Collect(ctx, attachments.List(client, opts), f.limit, attachments.ExtractAttachments)
	if err != nil {
		return fmt.Errorf("listing volume attachments: %w", err)
	}
	t := output.Table{
		Columns: []string{"ID", "Volume ID", "Server ID", "Status"},
		Rows:    make([][]any, 0, len(all)),
	}
	for _, at := range all {
		t.Rows = append(t.Rows, []any{at.ID, at.VolumeID, at.Instance, at.Status})
	}
	return o.WriteList(w, t)
}

func newAttachmentShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <attachment>",
		Short: "Show volume attachment details",
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
			return runAttachmentShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

// runAttachmentShow fetches one attachment. Attachments have no name, so the ID
// is used verbatim — there is nothing to resolve.
func runAttachmentShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	if err := requireVolumeMicroversion(client, attachmentsMicroversion, "volume attachment show"); err != nil {
		return err
	}
	at, err := attachments.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting volume attachment %q: %w", id, err)
	}
	fields, values := attachmentShowFields(at)
	return o.WriteSingle(w, fields, values)
}

// attachmentConnectorFlags holds the connector fields shared by "attachment
// create --connect" and "attachment set". They describe the host that will open
// the volume, and cinder hands them to the backend driver.
type attachmentConnectorFlags struct {
	initiator  string
	ip         string
	host       string
	platform   string
	osType     string
	multipath  bool
	mountpoint string
}

func (c *attachmentConnectorFlags) addFlags(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&c.initiator, "initiator", "", "iSCSI initiator IQN of the connecting host")
	fl.StringVar(&c.ip, "ip", "", "IP address of the connecting host")
	fl.StringVar(&c.host, "host", "", "hostname of the connecting host")
	fl.StringVar(&c.platform, "platform", "", "platform of the connecting host (e.g. x86_64)")
	fl.StringVar(&c.osType, "os-type", "", "OS type of the connecting host (e.g. linux2)")
	fl.BoolVar(&c.multipath, "multipath", false, "use multipath on the connecting host")
	fl.StringVar(&c.mountpoint, "mountpoint", "", "mountpoint of the volume on the connecting host")
}

// set reports whether any connector field was given a value. --multipath is
// excluded on purpose: it is a bool that defaults to false, so it cannot be told
// apart from "unset" and must never be the sole reason to send a connector.
func (c *attachmentConnectorFlags) set() bool {
	return c.initiator != "" || c.ip != "" || c.host != "" ||
		c.platform != "" || c.osType != "" || c.mountpoint != ""
}

// connector renders the flags as the cinder connector map. Only the fields the
// user supplied are emitted (upstream OSC sends the absent ones as null, which
// some backend drivers reject); multipath always rides along, as it does upstream.
func (c *attachmentConnectorFlags) connector() map[string]any {
	m := map[string]any{"multipath": c.multipath}
	for k, v := range map[string]string{
		"initiator":  c.initiator,
		"ip":         c.ip,
		"host":       c.host,
		"platform":   c.platform,
		"os_type":    c.osType,
		"mountpoint": c.mountpoint,
	} {
		if v != "" {
			m[k] = v
		}
	}
	return m
}

// attachmentCreateFlags holds the options accepted by "volume attachment create".
type attachmentCreateFlags struct {
	connector attachmentConnectorFlags
	connect   bool
	mode      string
}

func newAttachmentCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &attachmentCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create <volume> <server>",
		Short: "Create a volume attachment record for a server",
		Long: "Create a volume attachment record for a server.\n\n" +
			"Without --connect this only reserves the attachment; supply the connector " +
			"later with \"volume attachment set\" and finish with \"volume attachment complete\".",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newVolumeSession(ctx, a)
			if err != nil {
				return err
			}
			// The server lives in nova, so resolve a name→ID via compute.
			compute, err := session.Compute()
			if err != nil {
				return err
			}
			serverID, err := resolve.ServerID(ctx, compute, args[1])
			if err != nil {
				return err
			}
			return runAttachmentCreate(ctx, client, o, args[0], serverID, f, cmd.OutOrStdout())
		},
	}
	f.connector.addFlags(cmd)
	fl := cmd.Flags()
	fl.BoolVar(&f.connect, "connect", false, "make an active connection using the supplied connector information")
	fl.StringVar(&f.mode, "mode", "", `attachment mode: "rw" or "ro" (requires volume API 3.54 or later)`)
	return cmd
}

func runAttachmentCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	volumeRef, serverID string, f *attachmentCreateFlags, w io.Writer,
) error {
	if err := requireVolumeMicroversion(client, attachmentsMicroversion, "volume attachment create"); err != nil {
		return err
	}
	switch f.mode {
	case "", "rw", "ro":
	default:
		return fmt.Errorf(`invalid --mode %q: want "rw" or "ro"`, f.mode)
	}
	if f.mode != "" {
		if err := requireVolumeMicroversion(client, attachModeMicroversion, "--mode"); err != nil {
			return err
		}
	}
	// Connector information is only meaningful for an active connection, and
	// silently dropping it would leave the caller thinking the volume is connected.
	if !f.connect && f.connector.set() {
		return fmt.Errorf("connector flags (--initiator, --ip, --host, --platform, --os-type, --mountpoint) " +
			"require --connect")
	}
	volumeID, err := resolveVolumeID(ctx, client, volumeRef)
	if err != nil {
		return err
	}
	opts := attachments.CreateOpts{
		VolumeUUID:   volumeID,
		InstanceUUID: serverID,
		Mode:         f.mode,
	}
	// A nil connector is what makes this a reserve-only attachment, so only set it
	// when --connect was given.
	if f.connect {
		opts.Connector = f.connector.connector()
	}
	at, err := attachments.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating attachment for volume %q: %w", volumeRef, err)
	}
	fields, values := attachmentShowFields(at)
	return o.WriteSingle(w, fields, values)
}

func newAttachmentDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <attachment>...",
		Short: "Delete one or more volume attachments",
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
			return runAttachmentDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runAttachmentDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string, w io.Writer) error {
	if err := requireVolumeMicroversion(client, attachmentsMicroversion, "volume attachment delete"); err != nil {
		return err
	}
	return batchdelete.Each(ids, func(id string) error {
		if err := attachments.Delete(ctx, client, id).ExtractErr(); err != nil {
			return fmt.Errorf("deleting volume attachment %q: %w", id, err)
		}
		if _, err := fmt.Fprintf(w, "Deleted volume attachment: %s\n", id); err != nil {
			return err
		}
		return nil
	})
}

func newAttachmentSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &attachmentConnectorFlags{}
	cmd := &cobra.Command{
		Use:   "set <attachment>",
		Short: "Update the connector of a volume attachment",
		Long: "Update the connector of a volume attachment.\n\n" +
			"This is the second step of the reserve → update → complete flow: it supplies " +
			"the connector for an attachment created without --connect.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newVolumeClient(ctx, a)
			if err != nil {
				return err
			}
			return runAttachmentSet(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	f.addFlags(cmd)
	return cmd
}

func runAttachmentSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, f *attachmentConnectorFlags, w io.Writer,
) error {
	if err := requireVolumeMicroversion(client, attachmentsMicroversion, "volume attachment set"); err != nil {
		return err
	}
	// Cinder replaces the connector wholesale, and a connector carrying nothing but
	// multipath is never what the caller meant.
	if !f.set() {
		return fmt.Errorf("nothing to set: specify at least one of --initiator, --ip, --host, " +
			"--platform, --os-type, --mountpoint")
	}
	at, err := attachments.Update(ctx, client, id, attachments.UpdateOpts{Connector: f.connector()}).Extract()
	if err != nil {
		return fmt.Errorf("updating volume attachment %q: %w", id, err)
	}
	fields, values := attachmentShowFields(at)
	return o.WriteSingle(w, fields, values)
}

func newAttachmentCompleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "complete <attachment>",
		Short: "Mark a volume attachment as completed (volume API 3.44+)",
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
			return runAttachmentComplete(ctx, client, args[0], cmd.OutOrStdout())
		},
	}
}

func runAttachmentComplete(ctx context.Context, client *gophercloud.ServiceClient, id string, w io.Writer) error {
	if err := requireVolumeMicroversion(client, completeMicroversion, "volume attachment complete"); err != nil {
		return err
	}
	if err := attachments.Complete(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("completing volume attachment %q: %w", id, err)
	}
	_, err := fmt.Fprintf(w, "Completed volume attachment: %s\n", id)
	return err
}
