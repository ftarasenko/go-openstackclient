package network

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/taas/tapmirrors"
	"github.com/gophercloud/gophercloud/v2/pagination"
)

// TapMirror is a tap-as-a-service mirror (the tap-mirror extension).
// gophercloud's tapmirrors.Directions types the tunnel IDs as JSON strings,
// but neutron-lib validates them as integers and neutron echoes what it was
// sent, so a numeric one fails to decode there; a map takes either.
type TapMirror struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	TenantID    string         `json:"tenant_id"`
	ProjectID   string         `json:"project_id"`
	PortID      string         `json:"port_id"`
	Directions  map[string]any `json:"directions"`
	RemoteIP    string         `json:"remote_ip"`
	MirrorType  string         `json:"mirror_type"`
}

func extractTapMirror(r interface{ ExtractInto(any) error }) (*TapMirror, error) {
	var s struct {
		TapMirror *TapMirror `json:"tap_mirror"`
	}
	if err := r.ExtractInto(&s); err != nil {
		return nil, err
	}
	if s.TapMirror == nil {
		return nil, errors.New("response carries no tap_mirror")
	}
	return s.TapMirror, nil
}

// tapMirrorPage replaces tapmirrors.TapMirrorPage, whose IsEmpty decodes
// through the typed Directions and so fails on a page with numeric tunnel
// IDs.
type tapMirrorPage struct {
	pagination.LinkedPageBase
}

func (p tapMirrorPage) NextPageURL() (string, error) {
	var s struct {
		Links []gophercloud.Link `json:"tap_mirrors_links"`
	}
	if err := p.ExtractInto(&s); err != nil {
		return "", err
	}
	return gophercloud.ExtractNextURL(s.Links)
}

func (p tapMirrorPage) IsEmpty() (bool, error) {
	if p.StatusCode == http.StatusNoContent {
		return true, nil
	}
	all, err := extractTapMirrors(p)
	return len(all) == 0, err
}

func extractTapMirrors(page pagination.Page) ([]TapMirror, error) {
	p, ok := page.(tapMirrorPage)
	if !ok {
		return nil, fmt.Errorf("unexpected tap mirror page type %T", page)
	}
	var all []TapMirror
	err := p.ExtractIntoSlicePtr(&all, "tap_mirrors")
	return all, err
}

func listTapMirrors(ctx context.Context, c *gophercloud.ServiceClient, opts tapmirrors.ListOptsBuilder) ([]TapMirror, error) {
	u := c.ServiceURL("taas", "tap_mirrors")
	if opts != nil {
		q, err := opts.ToTapMirrorListQuery()
		if err != nil {
			return nil, err
		}
		u += q
	}
	pages, err := pagination.NewPager(c, u, func(r pagination.PageResult) pagination.Page {
		return tapMirrorPage{pagination.LinkedPageBase{PageResult: r}}
	}).AllPages(ctx)
	return extracted(pages, err, extractTapMirrors)
}

// FindTapMirrorID resolves a tap mirror name or ID to an ID.
func FindTapMirrorID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "tap mirror", ref, func(ctx context.Context) ([]TapMirror, error) {
		return listTapMirrors(ctx, c, tapmirrors.ListOpts{Name: ref})
	}, func(m TapMirror) string { return m.ID })
}

// ListTapMirrors lists every tap mirror matching opts; nil lists them all.
func ListTapMirrors(ctx context.Context, c *gophercloud.ServiceClient, opts tapmirrors.ListOptsBuilder) ([]TapMirror, error) {
	all, err := listTapMirrors(ctx, c, opts)
	return all, wrap(err, "listing tap mirrors")
}

// GetTapMirror reads one tap mirror.
func GetTapMirror(ctx context.Context, c *gophercloud.ServiceClient, id string) (*TapMirror, error) {
	m, err := extractTapMirror(tapmirrors.Get(ctx, c, id))
	return m, wrap(err, "getting tap mirror %s", id)
}

// CreateTapMirror creates a tap mirror. tapmirrors.CreateOpts always sends
// a name, lacks project_id and types the directions as integers; pass a
// builder of your own for any of those.
func CreateTapMirror(ctx context.Context, c *gophercloud.ServiceClient, opts tapmirrors.CreateOptsBuilder) (*TapMirror, error) {
	m, err := extractTapMirror(tapmirrors.Create(ctx, c, opts))
	return m, wrap(err, "creating tap mirror")
}

// UpdateTapMirror updates a tap mirror's name or description, the only
// attributes neutron lets it change.
func UpdateTapMirror(ctx context.Context, c *gophercloud.ServiceClient, id string, opts tapmirrors.UpdateOptsBuilder) (*TapMirror, error) {
	m, err := extractTapMirror(tapmirrors.Update(ctx, c, id, opts))
	return m, wrap(err, "updating tap mirror %s", id)
}

// DeleteTapMirror deletes a tap mirror.
func DeleteTapMirror(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(tapmirrors.Delete(ctx, c, id).ExtractErr(), "deleting tap mirror %s", id)
}
