package compute

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/remoteconsoles"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/secgroups"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/volumeattach"
)

// Server actions take the server's ID; resolve a name first
// (resolve.ServerID).

// SupportsMicroversion reports whether c's microversion is at least want.
// "latest" supports everything; an unset microversion is nova's 2.1 baseline.
func SupportsMicroversion(c *gophercloud.ServiceClient, want string) bool {
	if c.Microversion == "latest" {
		return true
	}
	hMaj, hMin, ok := parseMicroversion(c.Microversion)
	if !ok {
		return false
	}
	wMaj, wMin, _ := parseMicroversion(want)
	if hMaj != wMaj {
		return hMaj > wMaj
	}
	return hMin >= wMin
}

func parseMicroversion(v string) (major, minor int, ok bool) {
	majStr, minStr, found := strings.Cut(v, ".")
	if !found {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(majStr)
	minor, err2 := strconv.Atoi(minStr)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// StartServer starts a stopped server.
func StartServer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.Start(ctx, c, id).ExtractErr(), "starting server %s", id)
}

// StopServer stops a server.
func StopServer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.Stop(ctx, c, id).ExtractErr(), "stopping server %s", id)
}

// PauseServer pauses a server.
func PauseServer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.Pause(ctx, c, id).ExtractErr(), "pausing server %s", id)
}

// UnpauseServer unpauses a paused server.
func UnpauseServer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.Unpause(ctx, c, id).ExtractErr(), "unpausing server %s", id)
}

// SuspendServer suspends a server.
func SuspendServer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.Suspend(ctx, c, id).ExtractErr(), "suspending server %s", id)
}

// ResumeServer resumes a suspended server.
func ResumeServer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.Resume(ctx, c, id).ExtractErr(), "resuming server %s", id)
}

// LockServer locks a server against actions by non-admin users.
func LockServer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.Lock(ctx, c, id).ExtractErr(), "locking server %s", id)
}

// UnlockServer unlocks a locked server.
func UnlockServer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.Unlock(ctx, c, id).ExtractErr(), "unlocking server %s", id)
}

// UnrescueServer returns a rescued server to active.
func UnrescueServer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.Unrescue(ctx, c, id).ExtractErr(), "unrescuing server %s", id)
}

// RebootServer reboots a server, soft (through the guest) or hard (power
// cycle).
func RebootServer(ctx context.Context, c *gophercloud.ServiceClient, id string, method servers.RebootMethod) error {
	return wrap(servers.Reboot(ctx, c, id, servers.RebootOpts{Type: method}).ExtractErr(), "rebooting server %s", id)
}

// ResizeServer resizes a server to the flavor with ID flavorID. The server
// then waits in VERIFY_RESIZE for [ConfirmResize] or [RevertResize].
func ResizeServer(ctx context.Context, c *gophercloud.ServiceClient, id, flavorID string) error {
	return wrap(servers.Resize(ctx, c, id, servers.ResizeOpts{FlavorRef: flavorID}).ExtractErr(), "resizing server %s", id)
}

// ConfirmResize confirms a pending resize or cold migration.
func ConfirmResize(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.ConfirmResize(ctx, c, id).ExtractErr(), "confirming resize of server %s", id)
}

// RevertResize reverts a pending resize or cold migration.
func RevertResize(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.RevertResize(ctx, c, id).ExtractErr(), "reverting resize of server %s", id)
}

// RebuildUserDataMicroversion is the nova microversion that added user_data
// to the rebuild request.
const RebuildUserDataMicroversion = "2.57"

// RebuildOpts adds user_data to a rebuild. gophercloud's servers.RebuildOpts
// predates microversion 2.57 and has no field for it.
type RebuildOpts struct {
	servers.RebuildOptsBuilder

	// UserData is the base64 payload to set; nil leaves user_data out, which
	// keeps the server's own. ClearUserData sends JSON null, nova's reset.
	// Either needs [RebuildUserDataMicroversion].
	UserData      *string
	ClearUserData bool
}

// ToServerRebuildMap implements servers.RebuildOptsBuilder.
func (opts RebuildOpts) ToServerRebuildMap() (map[string]any, error) {
	body, err := opts.RebuildOptsBuilder.ToServerRebuildMap()
	if err != nil {
		return nil, err
	}
	rebuild, ok := body["rebuild"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected rebuild request body: %T", body["rebuild"])
	}
	switch {
	case opts.ClearUserData:
		rebuild["user_data"] = nil
	case opts.UserData != nil:
		rebuild["user_data"] = *opts.UserData
	}
	return body, nil
}

// RebuildServer rebuilds a server from an image (servers.RebuildOpts or
// [RebuildOpts]).
func RebuildServer(ctx context.Context, c *gophercloud.ServiceClient, id string, opts servers.RebuildOptsBuilder) (*servers.Server, error) {
	s, err := servers.Rebuild(ctx, c, id, opts).Extract()
	if err != nil {
		return nil, fmt.Errorf("rebuilding server %s: %w", id, err)
	}
	return s, nil
}

// VolumeAttachOpts is a volume attachment request. gophercloud's
// volumeattach.CreateOpts tags DeleteOnTermination omitempty, so it cannot
// send false.
type VolumeAttachOpts struct {
	VolumeID string
	// Device is the guest device name; empty lets nova choose.
	Device string
	// Tag needs microversion 2.49.
	Tag string
	// DeleteOnTermination, when set, needs microversion 2.79; nil keeps
	// nova's default (preserve the volume).
	DeleteOnTermination *bool
}

// ToVolumeAttachmentCreateMap implements volumeattach.CreateOptsBuilder.
func (o VolumeAttachOpts) ToVolumeAttachmentCreateMap() (map[string]any, error) {
	att := map[string]any{"volumeId": o.VolumeID}
	if o.Device != "" {
		att["device"] = o.Device
	}
	if o.Tag != "" {
		att["tag"] = o.Tag
	}
	if o.DeleteOnTermination != nil {
		att["delete_on_termination"] = *o.DeleteOnTermination
	}
	return map[string]any{"volumeAttachment": att}, nil
}

// AttachVolume attaches a volume to a server. Nova used to answer 200 with
// the attachment; a current one answers 202 with no body and attaches
// asynchronously, which volumeattach.Create reports as a failure. Both are
// accepted here, so read the attachment back if you need it.
func AttachVolume(ctx context.Context, c *gophercloud.ServiceClient, serverID string, opts volumeattach.CreateOptsBuilder) error {
	body, err := opts.ToVolumeAttachmentCreateMap()
	if err != nil {
		return fmt.Errorf("attaching volume to server %s: %w", serverID, err)
	}
	resp, err := c.Post(ctx, c.ServiceURL("servers", serverID, "os-volume_attachments"), body, nil,
		&gophercloud.RequestOpts{OkCodes: []int{http.StatusOK, http.StatusAccepted}})
	if resp != nil {
		_ = resp.Body.Close()
	}
	return wrap(err, "attaching volume to server %s", serverID)
}

// DetachVolume detaches a volume from a server.
func DetachVolume(ctx context.Context, c *gophercloud.ServiceClient, serverID, volumeID string) error {
	return wrap(volumeattach.Delete(ctx, c, serverID, volumeID).ExtractErr(),
		"detaching volume %s from server %s", volumeID, serverID)
}

// FloatingIPActionMicroversion is the microversion the floating-IP server
// actions are sent at: nova removed addFloatingIp and removeFloatingIp at
// 2.44.
const FloatingIPActionMicroversion = "2.43"

// AddFloatingIP associates a floating IP with a server, with fixedAddress
// when it is not empty. Prefer neutron (associate the floating IP with the
// port) on new code; this is nova's proxy, pinned to
// [FloatingIPActionMicroversion] whatever c's microversion.
func AddFloatingIP(ctx context.Context, c *gophercloud.ServiceClient, id, address, fixedAddress string) error {
	action := map[string]any{"address": address}
	if fixedAddress != "" {
		action["fixed_address"] = fixedAddress
	}
	return wrap(floatingIPAction(ctx, c, id, map[string]any{"addFloatingIp": action}),
		"associating floating IP %q with server %s", address, id)
}

// RemoveFloatingIP disassociates a floating IP from a server, through
// nova's proxy pinned to [FloatingIPActionMicroversion].
func RemoveFloatingIP(ctx context.Context, c *gophercloud.ServiceClient, id, address string) error {
	return wrap(floatingIPAction(ctx, c, id, map[string]any{"removeFloatingIp": map[string]any{"address": address}}),
		"disassociating floating IP %q from server %s", address, id)
}

// floatingIPAction posts a server action on a shallow copy of c pinned to
// FloatingIPActionMicroversion. gophercloud has no typed call for these
// actions, and RequestOpts.MoreHeaders cannot pin one request: the client's
// Microversion overwrites the header on every request.
func floatingIPAction(ctx context.Context, c *gophercloud.ServiceClient, id string, body map[string]any) error {
	legacy := *c
	legacy.Microversion = FloatingIPActionMicroversion
	return serverAction(ctx, &legacy, id, body)
}

// serverAction posts a raw server action at c's microversion.
func serverAction(ctx context.Context, c *gophercloud.ServiceClient, id string, body map[string]any) error {
	resp, err := c.Post(ctx, c.ServiceURL("servers", id, "action"), body, nil, &gophercloud.RequestOpts{OkCodes: []int{200, 202}})
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	_, _, err = gophercloud.ParseResponse(resp, err)
	return err
}

// AddSecurityGroup adds a security group, by name or ID, to every port of a
// server.
func AddSecurityGroup(ctx context.Context, c *gophercloud.ServiceClient, id, group string) error {
	return wrap(secgroups.AddServer(ctx, c, id, group).ExtractErr(), "adding security group %q to server %s", group, id)
}

// RemoveSecurityGroup removes a security group from a server.
func RemoveSecurityGroup(ctx context.Context, c *gophercloud.ServiceClient, id, group string) error {
	return wrap(secgroups.RemoveServer(ctx, c, id, group).ExtractErr(), "removing security group %q from server %s", group, id)
}

// AddServerGroup adds a running server to a server group, migrating it when
// the group's policy requires: KeyStack's addServerGroup action, which
// vanilla nova rejects with 400. The call returns while a migration may still
// run; the server is a member once nova's task clears and the group lists it.
func AddServerGroup(ctx context.Context, c *gophercloud.ServiceClient, id, groupID string) error {
	body := map[string]any{"addServerGroup": map[string]any{"server_group_id": groupID}}
	return wrap(serverAction(ctx, c, id, body), "adding server %s to server group %s", id, groupID)
}

// RemoveServerGroup removes a server from its server group: KeyStack's
// removeServerGroup action.
func RemoveServerGroup(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(serverAction(ctx, c, id, map[string]any{"removeServerGroup": nil}),
		"removing server %s from its server group", id)
}

// ConsoleOutput reads a server's console log, the last lines lines of it
// when lines is above zero.
func ConsoleOutput(ctx context.Context, c *gophercloud.ServiceClient, id string, lines int) (string, error) {
	out, err := servers.ShowConsoleOutput(ctx, c, id, servers.ShowConsoleOutputOpts{Length: lines}).Extract()
	if err != nil {
		return "", fmt.Errorf("fetching console log for server %s: %w", id, err)
	}
	return out, nil
}

// CreateRemoteConsole mints a remote console of type t for a server, with
// the protocol that type needs.
func CreateRemoteConsole(ctx context.Context, c *gophercloud.ServiceClient, id string, t remoteconsoles.ConsoleType) (*remoteconsoles.RemoteConsole, error) {
	protocol := remoteconsoles.ConsoleProtocolVNC
	switch t {
	case remoteconsoles.ConsoleTypeSPICEHTML5:
		protocol = remoteconsoles.ConsoleProtocolSPICE
	case remoteconsoles.ConsoleTypeSerial:
		protocol = remoteconsoles.ConsoleProtocolSerial
	case remoteconsoles.ConsoleTypeWebMKS:
		protocol = remoteconsoles.ConsoleProtocolMKS
	}
	rc, err := remoteconsoles.Create(ctx, c, id, remoteconsoles.CreateOpts{Protocol: protocol, Type: t}).Extract()
	if err != nil {
		return nil, fmt.Errorf("creating console for server %s: %w", id, err)
	}
	return rc, nil
}

// wrap prefixes a non-nil err with the formatted call description.
func wrap(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}
