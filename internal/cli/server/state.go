package server

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/resolve"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Server state verbs that park or recover an instance: shelve, unshelve,
// rescue, unrescue, and the snapshot verb `server image create`.
//
// Flag names follow upstream OSC (`openstack server shelve|unshelve|rescue|
// unrescue`, `openstack server image create`). UNVERIFIED against KeyStack docs
// (https://docs.keystack.ru/ returned HTTP 403 at implementation time); falls
// back to upstream OSC semantics.

// statusPollInterval and statusPollTimeout bound the --wait loops. Vars, not
// consts, so tests can shorten the interval.
var (
	statusPollInterval = 5 * time.Second
	statusPollTimeout  = 10 * time.Minute
)

// --- shelve -----------------------------------------------------------------

func newServerShelveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var offload, wait bool
	var waitTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "shelve <server> [<server> ...]",
		Short: "Shelve server(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerShelve(ctx, client, args, offload, wait, waitTimeout, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&offload, "offload", false, "also offload the server, releasing its host resources immediately")
	fl.BoolVar(&wait, "wait", false, "wait for the shelve (and offload) to complete")
	fl.DurationVar(&waitTimeout, flagWaitTimeout, statusPollTimeout, helpWaitTimeout)
	return cmd
}

// runServerShelve shelves each server. --offload issues shelveOffload, whose
// resting state is SHELVED_OFFLOADED rather than SHELVED; without it nova
// offloads on its own schedule (shelved_offload_time), so --wait accepts either
// resting state and does not sit through a delay it cannot influence.
func runServerShelve(ctx context.Context, client *gophercloud.ServiceClient, refs []string,
	offload, wait bool, waitTimeout time.Duration, w io.Writer,
) error {
	// Either is a finished shelve: nova may offload immediately or after
	// shelved_offload_time, and both are past the point of no return.
	want := []string{"SHELVED", "SHELVED_OFFLOADED"}
	verb := "Shelved"
	// Shelve and ShelveOffload return different result types, so the action is
	// wrapped rather than assigned directly.
	action := func(ctx context.Context, client *gophercloud.ServiceClient, id string) error {
		return servers.Shelve(ctx, client, id).ExtractErr()
	}
	if offload {
		// Explicitly asked for, so only the offloaded state will do.
		want = []string{"SHELVED_OFFLOADED"}
		action = func(ctx context.Context, client *gophercloud.ServiceClient, id string) error {
			return servers.ShelveOffload(ctx, client, id).ExtractErr()
		}
	}
	for _, ref := range refs {
		id, err := resolveServerID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := action(ctx, client, id); err != nil {
			return fmt.Errorf("shelving server %q: %w", ref, err)
		}
		if wait {
			if err := waitForServerStatuses(ctx, client, id, want, waitTimeout); err != nil {
				return fmt.Errorf("waiting for server %q to shelve: %w", ref, err)
			}
		}
		if _, err := fmt.Fprintf(w, "%s server %s\n", verb, ref); err != nil {
			return err
		}
	}
	return nil
}

// --- unshelve ---------------------------------------------------------------

// unshelveOpts extends gophercloud's UnshelveOpts with `host`, which nova
// accepts from microversion 2.91 and the typed struct does not model.
type unshelveOpts struct {
	servers.UnshelveOpts
	Host string
}

func (o unshelveOpts) ToUnshelveMap() (map[string]any, error) {
	body, err := o.UnshelveOpts.ToUnshelveMap()
	if err != nil {
		return nil, err
	}
	if o.Host == "" {
		return body, nil
	}
	// gophercloud renders {"unshelve": nil} when no field is set, so the inner
	// object may need creating before the host can be added.
	inner, _ := body["unshelve"].(map[string]any)
	if inner == nil {
		inner = map[string]any{}
	}
	inner["host"] = o.Host
	body["unshelve"] = inner
	return body, nil
}

type unshelveFlags struct {
	az          string
	host        string
	wait        bool
	waitTimeout time.Duration
}

func newServerUnshelveCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &unshelveFlags{}
	cmd := &cobra.Command{
		Use:   "unshelve <server> [<server> ...]",
		Short: "Unshelve server(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newComputeClient(ctx, a)
			if err != nil {
				return err
			}
			return runServerUnshelve(ctx, client, args, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.az, "availability-zone", "", "availability zone to unshelve into (nova 2.77 or later)")
	fl.StringVar(&f.host, "host", "", "host to unshelve onto (nova 2.91 or later)")
	fl.BoolVar(&f.wait, "wait", false, "wait for the unshelve to complete")
	fl.DurationVar(&f.waitTimeout, flagWaitTimeout, statusPollTimeout, helpWaitTimeout)
	return cmd
}

func runServerUnshelve(ctx context.Context, client *gophercloud.ServiceClient, refs []string,
	f *unshelveFlags, w io.Writer,
) error {
	opts := unshelveOpts{
		UnshelveOpts: servers.UnshelveOpts{AvailabilityZone: f.az},
		Host:         f.host,
	}
	for _, ref := range refs {
		id, err := resolveServerID(ctx, client, ref)
		if err != nil {
			return err
		}
		if err := servers.Unshelve(ctx, client, id, opts).ExtractErr(); err != nil {
			return fmt.Errorf("unshelving server %q: %w", ref, err)
		}
		if f.wait {
			if err := waitForServerStatus(ctx, client, id, "ACTIVE", f.waitTimeout); err != nil {
				return fmt.Errorf("waiting for server %q to unshelve: %w", ref, err)
			}
		}
		if _, err := fmt.Fprintf(w, "Unshelved server %s\n", ref); err != nil {
			return err
		}
	}
	return nil
}

// --- rescue / unrescue ------------------------------------------------------

type rescueFlags struct {
	image    string
	password string
}

func newServerRescueCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &rescueFlags{}
	cmd := &cobra.Command{
		Use:   "rescue <server>",
		Short: "Boot a server into its rescue image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			return runServerRescue(ctx, s.client, s.auth, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.image, "image", "", "image to rescue with (name or ID; default: the server's own image)")
	fl.StringVar(&f.password, flagPassword, "", "administrative password for the rescued server (default: generated)")
	return cmd
}

// runServerRescue rescues the server and prints the admin password nova
// returns. When the password was generated rather than supplied, this response
// is the only place it ever appears, so it goes to the output layer as a
// single-field result rather than a log line.
func runServerRescue(ctx context.Context, client *gophercloud.ServiceClient, ac *auth.Client,
	o *output.Options, ref string, f *rescueFlags, w io.Writer,
) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	imageID, err := resolveRescueImageID(ctx, ac, f.image)
	if err != nil {
		return err
	}
	adminPass, err := servers.Rescue(ctx, client, id, servers.RescueOpts{
		AdminPass:      f.password,
		RescueImageRef: imageID,
	}).Extract()
	if err != nil {
		return fmt.Errorf("rescuing server %q: %w", ref, err)
	}
	return o.WriteSingle(w, []string{"adminPass"}, []any{adminPass})
}

// resolveRescueImageID turns an image name into an ID via glance, since nova
// takes only a reference. An empty flag stays empty: nova then rescues with the
// server's own image.
func resolveRescueImageID(ctx context.Context, ac *auth.Client, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	imageClient, err := ac.Image()
	if err != nil {
		return "", err
	}
	return resolve.ImageID(ctx, imageClient, ref)
}

func newServerUnrescueCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newSimpleActionCommand(a, o, "unrescue", "Return a rescued server to active", "Unrescued",
		func(ctx context.Context, client *gophercloud.ServiceClient, id string) error {
			return servers.Unrescue(ctx, client, id).ExtractErr()
		})
}

// --- server image create ----------------------------------------------------

// newServerImageCommand builds "server image create". Upstream spells it as
// three words, so "image" is a nested parent under "server".
type serverImageCreateFlags struct {
	name        string
	properties  []string
	wait        bool
	waitTimeout time.Duration
}

func newServerImageCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &serverImageCreateFlags{}
	create := &cobra.Command{
		Use:   "create <server>",
		Short: "Create a new disk image from a running server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := newComputeSession(ctx, a)
			if err != nil {
				return err
			}
			return runServerImageCreate(ctx, s.client, s.auth, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := create.Flags()
	fl.StringVar(&f.name, "name", "", "name of the new image (default: the server's name)")
	fl.StringArrayVar(&f.properties, "property", nil, "key=value property to set on the image (repeatable)")
	fl.BoolVar(&f.wait, "wait", false, "wait until the image becomes active")
	fl.DurationVar(&f.waitTimeout, flagWaitTimeout, statusPollTimeout, helpWaitTimeout)

	cmd := &cobra.Command{Use: "image", Short: "Manage images created from a server"}
	cmd.AddCommand(create)
	return cmd
}

func runServerImageCreate(ctx context.Context, client *gophercloud.ServiceClient, ac *auth.Client,
	o *output.Options, ref string, f *serverImageCreateFlags, w io.Writer,
) error {
	id, err := resolveServerID(ctx, client, ref)
	if err != nil {
		return err
	}
	name := f.name
	if name == "" {
		// Upstream defaults the image name to the server's name; that needs the
		// server, so it is only fetched when the flag was omitted.
		s, err := servers.Get(ctx, client, id).Extract()
		if err != nil {
			return fmt.Errorf("reading server %q: %w", ref, err)
		}
		name = s.Name
	}
	metadata, err := parseStringMap(f.properties)
	if err != nil {
		return fmt.Errorf("parsing --property: %w", err)
	}
	imageID, err := servers.CreateImage(ctx, client, id, servers.CreateImageOpts{
		Name:     name,
		Metadata: metadata,
	}).ExtractImageID()
	if err != nil {
		return fmt.Errorf("creating an image from server %q: %w", ref, err)
	}
	if f.wait {
		if err := waitForImageActive(ctx, ac, imageID, f.waitTimeout); err != nil {
			return err
		}
	}
	return o.WriteSingle(w, []string{"id", "name"}, []any{imageID, name})
}

// parseStringMap turns repeated key=value flag values into a map. Nova's image
// metadata is string-to-string, unlike the free-form maps elsewhere.
func parseStringMap(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("expected key=value, got %q", p)
		}
		m[strings.TrimSpace(k)] = v
	}
	return m, nil
}

// --- polling ----------------------------------------------------------------

// waitForServerStatus polls until the server reports want, fails fast on ERROR,
// and gives up at the timeout. Nova reports transitional states (SHELVING,
// UNSHELVING) in between, which are neither success nor failure.
func waitForServerStatus(ctx context.Context, client *gophercloud.ServiceClient, id, want string, timeout time.Duration) error {
	return waitForServerStatuses(ctx, client, id, []string{want}, timeout)
}

// waitForServerStatuses is waitForServerStatus over a set of acceptable resting
// states, for the transitions that have more than one.
//
// Shelve is the case that needs it: nova moves ACTIVE → SHELVED → and then, when
// shelved_offload_time is 0 (a common setting, and the default in several
// distributions), straight on to SHELVED_OFFLOADED. Waiting for exactly
// "SHELVED" then depends on catching a window narrower than the 5s poll
// interval, and missing it means spinning until --wait-timeout for a status the
// server has already passed through and will never report again.
func waitForServerStatuses(ctx context.Context, client *gophercloud.ServiceClient, id string, want []string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = statusPollTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(statusPollInterval)
	defer ticker.Stop()

	accept := make(map[string]bool, len(want))
	for _, s := range want {
		accept[s] = true
	}
	wanted := strings.Join(want, " or ")

	var last string
	for {
		s, err := servers.Get(ctx, client, id).Extract()
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for status %q%s: %w", wanted, lastStatus(last), ctx.Err())
			}
			return err
		}
		last = s.Status
		switch {
		case accept[s.Status]:
			return nil
		case s.Status == "ERROR":
			return fmt.Errorf("server entered ERROR status while waiting for %q", wanted)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for status %q%s: %w", wanted, lastStatus(last), ctx.Err())
		case <-ticker.C:
		}
	}
}

// waitForImageActive polls glance rather than nova: nova returns as soon as the
// snapshot is queued, and the image is not usable until glance reports active.
func waitForImageActive(ctx context.Context, ac *auth.Client, imageID string, timeout time.Duration) error {
	imageClient, err := ac.Image()
	if err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = statusPollTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(statusPollInterval)
	defer ticker.Stop()

	var last string
	for {
		img, err := images.Get(ctx, imageClient, imageID).Extract()
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for image %s to become active%s: %w", imageID, lastStatus(last), ctx.Err())
			}
			return err
		}
		last = string(img.Status)
		switch img.Status {
		case images.ImageStatusActive:
			return nil
		case images.ImageStatusKilled, images.ImageStatusDeleted:
			return fmt.Errorf("image %s entered status %q instead of active", imageID, img.Status)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for image %s to become active%s: %w", imageID, lastStatus(last), ctx.Err())
		case <-ticker.C:
		}
	}
}

func lastStatus(last string) string {
	if last == "" {
		return ""
	}
	return fmt.Sprintf(" (last status %q)", last)
}
