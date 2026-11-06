package compute

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/pagination"

	"github.com/ftarasenko/koc/pkg/openstack/paging"
)

// ServerListOpts adds to servers.ListOpts the filters it has no fields for.
// Each is sent only when set.
type ServerListOpts struct {
	servers.ListOpts
	// Deleted lists only deleted servers (admin).
	Deleted bool
	// CreatedSince, CreatedBefore, DeletedSince and DeletedBefore are a
	// KeyStack extension (2.66+, ISO 8601); nova rejects them.
	CreatedSince  string
	CreatedBefore string
	DeletedSince  string
	DeletedBefore string
	// ChangesBefore is nova's own updated_at bound (2.66+).
	ChangesBefore string
	// ExcludeDeleted sends deleted=false. Nova includes deleted servers in a
	// changes-since or changes-before window unless told otherwise. Deleted
	// wins over it.
	ExcludeDeleted bool
}

// ToServerListQuery implements servers.ListOptsBuilder.
func (o ServerListOpts) ToServerListQuery() (string, error) {
	base, err := o.ListOpts.ToServerListQuery()
	if err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	vals := u.Query()
	switch {
	case o.Deleted:
		vals.Set("deleted", "true")
	case o.ExcludeDeleted:
		vals.Set("deleted", "false")
	}
	for key, val := range map[string]string{
		"created-since":  o.CreatedSince,
		"created-before": o.CreatedBefore,
		"deleted-since":  o.DeletedSince,
		"deleted-before": o.DeletedBefore,
		"changes-before": o.ChangesBefore,
	} {
		if val != "" {
			vals.Set(key, val)
		}
	}
	u.RawQuery = vals.Encode()
	return u.String(), nil
}

// ListServers lists servers in detail. limit > 0 caps the result; nova takes
// limit only as a page size.
//
// Every microversion widens each entry (2.3 adds user_data), so a caller that
// reads only a few fields can list on a copy of c pinned lower.
func ListServers(ctx context.Context, c *gophercloud.ServiceClient, opts servers.ListOptsBuilder, limit int) ([]servers.Server, error) {
	return listServers(ctx, servers.List(c, opts), limit, servers.ExtractServers)
}

// ListServerNames lists servers without details: each carries only its ID,
// name and links.
func ListServerNames(ctx context.Context, c *gophercloud.ServiceClient, opts servers.ListOptsBuilder, limit int) ([]servers.Server, error) {
	return listServers(ctx, servers.ListSimple(c, opts), limit, servers.ExtractServers)
}

// ListServersRaw is [ListServers] that also returns each server's attributes
// as nova sent them, keyed by ID, for the ones servers.Server does not model.
func ListServersRaw(ctx context.Context, c *gophercloud.ServiceClient, opts servers.ListOptsBuilder, limit int,
) ([]servers.Server, map[string]map[string]any, error) {
	raw := map[string]map[string]any{}
	all, err := listServers(ctx, servers.List(c, opts), limit, func(page pagination.Page) ([]servers.Server, error) {
		list, err := servers.ExtractServers(page)
		if err != nil {
			return nil, err
		}
		var body struct {
			Servers []map[string]any `json:"servers"`
		}
		sp, ok := page.(servers.ServerPage)
		if !ok {
			return nil, fmt.Errorf("unexpected page type %T", page)
		}
		if err := sp.ExtractInto(&body); err != nil {
			return nil, err
		}
		for _, m := range body.Servers {
			if id, ok := m["id"].(string); ok {
				raw[id] = m
			}
		}
		return list, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return all, raw, nil
}

func listServers(ctx context.Context, pager pagination.Pager, limit int,
	extract func(pagination.Page) ([]servers.Server, error),
) ([]servers.Server, error) {
	all, err := paging.Collect(ctx, pager, limit, extract)
	if err != nil {
		return nil, fmt.Errorf("listing servers: %w", err)
	}
	return all, nil
}

// GetServer reads a server.
func GetServer(ctx context.Context, c *gophercloud.ServiceClient, id string) (*servers.Server, error) {
	s, err := servers.Get(ctx, c, id).Extract()
	if err != nil {
		return nil, fmt.Errorf("reading server %s: %w", id, err)
	}
	return s, nil
}

// GetServerRaw reads a server as nova sent it. servers.Server drops the
// OS-EXT-* admin attributes, among others.
func GetServerRaw(ctx context.Context, c *gophercloud.ServiceClient, id string) (map[string]any, error) {
	var body struct {
		Server map[string]any `json:"server"`
	}
	if err := getJSON(ctx, c, c.ServiceURL("servers", id), &body); err != nil {
		return nil, fmt.Errorf("showing server %s: %w", id, err)
	}
	return body.Server, nil
}

// EncodeUserData base64-encodes user data for servers.CreateOpts.UserData.
// Encode before handing it over: CreateOpts encodes only what does not
// already decode as base64, and plain text such as "hostname\n" does.
func EncodeUserData(raw []byte) []byte {
	out := make([]byte, base64.StdEncoding.EncodedLen(len(raw)))
	base64.StdEncoding.Encode(out, raw)
	return out
}

// ServerCreateOptsExt adds to a create what servers.CreateOpts cannot carry.
type ServerCreateOptsExt struct {
	servers.CreateOptsBuilder
	// Host is the target compute host (admin, 2.74+).
	Host string
	// BlockDevice is block_device_mapping_v2, one map per mapping as nova
	// takes it.
	BlockDevice []map[string]any
}

// ToServerCreateMap implements servers.CreateOptsBuilder.
func (o ServerCreateOptsExt) ToServerCreateMap() (map[string]any, error) {
	body, err := o.CreateOptsBuilder.ToServerCreateMap()
	if err != nil {
		return nil, err
	}
	if o.Host == "" && len(o.BlockDevice) == 0 {
		return body, nil
	}
	srv, ok := body["server"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`server create body carries no "server" object`)
	}
	if o.Host != "" {
		srv["host"] = o.Host
	}
	if len(o.BlockDevice) > 0 {
		srv["block_device_mapping_v2"] = o.BlockDevice
	}
	return body, nil
}

// CreateServer creates a server; hints may be nil. Nova answers with the ID
// and the generated admin password only: read the rest with [GetServer] or
// [GetServerRaw], and wait for ACTIVE with [WaitForServerStatus].
func CreateServer(ctx context.Context, c *gophercloud.ServiceClient, opts servers.CreateOptsBuilder,
	hints servers.SchedulerHintOptsBuilder,
) (*servers.Server, error) {
	s, err := servers.Create(ctx, c, opts, hints).Extract()
	if err != nil {
		return nil, fmt.Errorf("creating server: %w", err)
	}
	return s, nil
}

// DeleteServer deletes a server. With reclaim_instance_interval set nova only
// soft-deletes it; [ForceDeleteServer] skips that.
func DeleteServer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.Delete(ctx, c, id).ExtractErr(), "deleting server %s", id)
}

// ForceDeleteServer deletes a server at once, soft-deleted or not.
func ForceDeleteServer(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(servers.ForceDelete(ctx, c, id).ExtractErr(), "deleting server %s", id)
}

// ServerUpdateOpts is a server update. servers.UpdateOpts has no description
// or hostname. A nil pointer leaves the field alone; an empty string clears
// it.
type ServerUpdateOpts struct {
	Name string `json:"name,omitempty"`
	// Description needs 2.19.
	Description *string `json:"description,omitempty"`
	// Hostname needs 2.90.
	Hostname *string `json:"hostname,omitempty"`
}

// ToServerUpdateMap implements servers.UpdateOptsBuilder.
func (o ServerUpdateOpts) ToServerUpdateMap() (map[string]any, error) {
	return gophercloud.BuildRequestBody(o, "server")
}

// UpdateServer updates a server's name, description or hostname.
func UpdateServer(ctx context.Context, c *gophercloud.ServiceClient, id string, opts servers.UpdateOptsBuilder) (*servers.Server, error) {
	s, err := servers.Update(ctx, c, id, opts).Extract()
	if err != nil {
		return nil, fmt.Errorf("updating server %s: %w", id, err)
	}
	return s, nil
}

// SetServerAvailabilityZone moves a server to another availability zone. It
// is a KeyStack extension (2.90+); nova rejects the field.
func SetServerAvailabilityZone(ctx context.Context, c *gophercloud.ServiceClient, id, zone string) error {
	body := map[string]any{"server": map[string]any{"availability_zone": zone}}
	r, err := c.Put(ctx, c.ServiceURL("servers", id), body, nil, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if r != nil {
		_ = r.Body.Close()
	}
	return wrap(err, "updating availability zone on server %s", id)
}

// UpdateServerMetadata sets metadata keys, keeping the others.
func UpdateServerMetadata(ctx context.Context, c *gophercloud.ServiceClient, id string, metadata map[string]string) (map[string]string, error) {
	m, err := servers.UpdateMetadata(ctx, c, id, servers.MetadataOpts(metadata)).Extract()
	if err != nil {
		return nil, fmt.Errorf("updating metadata on server %s: %w", id, err)
	}
	return m, nil
}

// ReplaceServerMetadata replaces all of a server's metadata; an empty map
// clears it.
func ReplaceServerMetadata(ctx context.Context, c *gophercloud.ServiceClient, id string, metadata map[string]string) (map[string]string, error) {
	m, err := servers.ResetMetadata(ctx, c, id, servers.MetadataOpts(metadata)).Extract()
	if err != nil {
		return nil, fmt.Errorf("replacing metadata on server %s: %w", id, err)
	}
	return m, nil
}

// DeleteServerMetadatum removes one metadata key.
func DeleteServerMetadatum(ctx context.Context, c *gophercloud.ServiceClient, id, key string) error {
	return wrap(servers.DeleteMetadatum(ctx, c, id, key).ExtractErr(), "removing metadata %q from server %s", key, id)
}

// ChangeAdminPassword sets the guest's admin password, if its hypervisor
// driver can.
func ChangeAdminPassword(ctx context.Context, c *gophercloud.ServiceClient, id, password string) error {
	return wrap(servers.ChangeAdminPassword(ctx, c, id, password).ExtractErr(), "changing admin password on server %s", id)
}

// ClearServerPassword drops the encrypted admin password nova published to
// the metadata service. The guest's password does not change.
func ClearServerPassword(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	r, err := c.Delete(ctx, c.ServiceURL("servers", id, "os-server-password"), &gophercloud.RequestOpts{OkCodes: []int{202, 204}})
	if r != nil {
		_ = r.Body.Close()
	}
	return wrap(err, "clearing admin password on server %s", id)
}

// ResetServerState sets a server's state to active or error (admin).
func ResetServerState(ctx context.Context, c *gophercloud.ServiceClient, id string, state servers.ServerState) error {
	return wrap(servers.ResetState(ctx, c, id, state).ExtractErr(), "resetting state of server %s to %q", id, state)
}

// AddServerTag tags a server (2.26+). Nova refuses a tag containing "/".
func AddServerTag(ctx context.Context, c *gophercloud.ServiceClient, id, tag string) error {
	r, err := c.Put(ctx, c.ServiceURL("servers", id, "tags", tag), nil, nil, &gophercloud.RequestOpts{OkCodes: []int{201, 204}})
	if r != nil {
		_ = r.Body.Close()
	}
	return wrap(err, "adding tag %q to server %s", tag, id)
}

// DeleteServerTag removes one tag from a server (2.26+).
func DeleteServerTag(ctx context.Context, c *gophercloud.ServiceClient, id, tag string) error {
	return wrap(deleteNoContent(ctx, c, c.ServiceURL("servers", id, "tags", tag)), "removing tag %q from server %s", tag, id)
}

// DeleteServerTags removes every tag from a server (2.26+).
func DeleteServerTags(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(deleteNoContent(ctx, c, c.ServiceURL("servers", id, "tags")), "removing all tags from server %s", id)
}

func deleteNoContent(ctx context.Context, c *gophercloud.ServiceClient, u string) error {
	r, err := c.Delete(ctx, u, &gophercloud.RequestOpts{OkCodes: []int{204}})
	if r != nil {
		_ = r.Body.Close()
	}
	return err
}
