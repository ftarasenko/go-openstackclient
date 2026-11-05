package compute

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
)

// ShelveServer shelves a server. Nova offloads it later on its own schedule
// (shelved_offload_time), so it rests in SHELVED or SHELVED_OFFLOADED.
func ShelveServer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.Shelve(ctx, c, id).ExtractErr(), "shelving server %s", id)
}

// ShelveOffloadServer shelves a server and offloads it now; it rests in
// SHELVED_OFFLOADED.
func ShelveOffloadServer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.ShelveOffload(ctx, c, id).ExtractErr(), "shelving server %s", id)
}

// UnshelveOpts adds host to an unshelve. gophercloud's servers.UnshelveOpts
// has no field for it.
type UnshelveOpts struct {
	servers.UnshelveOpts
	// Host is the compute host to unshelve onto; needs microversion 2.91.
	Host string
}

// ToUnshelveMap implements servers.UnshelveOptsBuilder.
func (o UnshelveOpts) ToUnshelveMap() (map[string]any, error) {
	body, err := o.UnshelveOpts.ToUnshelveMap()
	if err != nil {
		return nil, err
	}
	if o.Host == "" {
		return body, nil
	}
	// gophercloud renders {"unshelve": nil} when no field is set.
	inner, _ := body["unshelve"].(map[string]any)
	if inner == nil {
		inner = map[string]any{}
	}
	inner["host"] = o.Host
	body["unshelve"] = inner
	return body, nil
}

// UnshelveServer unshelves a server (servers.UnshelveOpts or
// [UnshelveOpts]).
func UnshelveServer(ctx context.Context, c *gophercloud.ServiceClient, id string, opts servers.UnshelveOptsBuilder) error {
	return wrap(servers.Unshelve(ctx, c, id, opts).ExtractErr(), "unshelving server %s", id)
}

// RescueServer boots a server into a rescue image and returns the admin
// password nova set: the one in opts, or a generated one, which this answer
// is the only place to read.
func RescueServer(ctx context.Context, c *gophercloud.ServiceClient, id string, opts servers.RescueOptsBuilder) (string, error) {
	pass, err := servers.Rescue(ctx, c, id, opts).Extract()
	if err != nil {
		return "", fmt.Errorf("rescuing server %s: %w", id, err)
	}
	return pass, nil
}

// CreateServerImage snapshots a server into a new image and returns its ID.
// Nova answers once the snapshot is queued; the image is usable once glance
// reports it active (image.WaitForStatus).
func CreateServerImage(ctx context.Context, c *gophercloud.ServiceClient, id string, opts servers.CreateImageOptsBuilder) (string, error) {
	imageID, err := servers.CreateImage(ctx, c, id, opts).ExtractImageID()
	if err != nil {
		return "", fmt.Errorf("creating an image from server %s: %w", id, err)
	}
	return imageID, nil
}

// WaitForServerStatus polls a server every interval until its status is one
// of want with no task running, and gives up when ctx is done (give it a
// deadline), when a read fails (the error as gophercloud reports it) or when
// the server reaches ERROR.
//
// A status alone is not a resting one: nova reports SHELVED while still
// offloading, and ACTIVE through a live migration, and refuses an action sent
// then with 409. Several statuses suit a transition with more than one end;
// a shelve, for one, may go straight on to SHELVED_OFFLOADED.
func WaitForServerStatus(ctx context.Context, c *gophercloud.ServiceClient, id string, want []string, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	accept := make(map[string]bool, len(want))
	for _, s := range want {
		accept[s] = true
	}
	wanted := strings.Join(want, " or ")

	var last string
	for {
		s, err := servers.Get(ctx, c, id).Extract()
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for status %q%s: %w", wanted, lastStatus(last), ctx.Err())
			}
			return err
		}
		last = s.Status
		if s.TaskState != "" {
			last += " (task " + s.TaskState + ")"
		}
		switch {
		case accept[s.Status] && s.TaskState == "":
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

// ErrServerSoftDeleted is what [WaitForServerDeleted] returns for a server
// nova parked in SOFT_DELETED: with reclaim_instance_interval set, nova
// deletes it for real only when that interval lapses. Delete it with
// servers.ForceDelete to skip the wait.
var ErrServerSoftDeleted = errors.New("server is SOFT_DELETED: nova defers the real delete until its reclaim interval lapses")

// WaitForServerDeleted polls a server every interval until nova answers 404,
// the point its ports and volumes are released. It gives up when ctx is done,
// when a read fails, and with [ErrServerSoftDeleted]. ERROR is not a failure
// here: a server deleted from ERROR stays ERROR until it is gone.
func WaitForServerDeleted(ctx context.Context, c *gophercloud.ServiceClient, id string, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var last string
	for {
		s, err := servers.Get(ctx, c, id).Extract()
		switch {
		case gophercloud.ResponseCodeIs(err, http.StatusNotFound):
			return nil
		case err != nil && ctx.Err() != nil:
			return fmt.Errorf("waiting for deletion%s: %w", lastStatus(last), ctx.Err())
		case err != nil:
			return err
		}
		// DELETED is not the 404 yet: nova can show the record for a moment.
		last = s.Status
		if s.Status == "SOFT_DELETED" {
			return ErrServerSoftDeleted
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for deletion%s: %w", lastStatus(last), ctx.Err())
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
