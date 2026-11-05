package dns

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gophercloud/gophercloud/v2"
)

// Blacklist is a zone-name pattern designate refuses to create.
type Blacklist struct {
	ID          string `json:"id"`
	Pattern     string `json:"pattern"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// TLD is a top-level domain designate allows zones under.
type TLD struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// listBlacklists is the unwrapped listing FindBlacklistID shares.
func listBlacklists(ctx context.Context, c *gophercloud.ServiceClient, pattern string, limit int) ([]Blacklist, error) {
	q, err := query(struct {
		Pattern string `q:"pattern"`
	}{pattern})
	if err != nil {
		return nil, err
	}
	return listAll(ctx, c, c.ServiceURL("blacklists")+q, limit,
		func(raw json.RawMessage) ([]Blacklist, string, error) {
			var page struct {
				Blacklists []Blacklist `json:"blacklists"`
				Links      links       `json:"links"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", fmt.Errorf("parsing blacklist list: %w", err)
			}
			return page.Blacklists, page.Links.Next, nil
		})
}

// ListBlacklists lists blacklists, filtered by exact pattern when pattern is
// set, stopping after limit results (0 = no cap).
func ListBlacklists(ctx context.Context, c *gophercloud.ServiceClient, pattern string, limit int) ([]Blacklist, error) {
	all, err := listBlacklists(ctx, c, pattern, limit)
	return all, wrap(err, "listing blacklists")
}

// FindBlacklistID resolves a blacklist pattern or ID to an ID.
func FindBlacklistID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "blacklist", ref, func(ctx context.Context) ([]Blacklist, error) {
		return listBlacklists(ctx, c, ref, 0)
	}, func(b Blacklist) string { return b.ID })
}

// GetBlacklist reads one blacklist.
func GetBlacklist(ctx context.Context, c *gophercloud.ServiceClient, id string) (*Blacklist, error) {
	var b Blacklist
	if err := getJSON(ctx, c, c.ServiceURL("blacklists", id), &b); err != nil {
		return nil, wrap(err, "showing blacklist %q", id)
	}
	return &b, nil
}

// CreateBlacklist creates a blacklist; an empty description is left out.
func CreateBlacklist(ctx context.Context, c *gophercloud.ServiceClient, pattern, description string) (*Blacklist, error) {
	body := map[string]any{"pattern": pattern}
	if description != "" {
		body["description"] = description
	}
	var b Blacklist
	if err := postJSON(ctx, c, c.ServiceURL("blacklists"), body, &b); err != nil {
		return nil, wrap(err, "creating blacklist %q", pattern)
	}
	return &b, nil
}

// UpdateBlacklist patches a blacklist with body's keys (pattern,
// description); a nil value clears that key.
func UpdateBlacklist(ctx context.Context, c *gophercloud.ServiceClient, id string, body map[string]any) (*Blacklist, error) {
	var b Blacklist
	if err := patchJSON(ctx, c, c.ServiceURL("blacklists", id), body, &b); err != nil {
		return nil, wrap(err, "updating blacklist %q", id)
	}
	return &b, nil
}

// DeleteBlacklist deletes a blacklist.
func DeleteBlacklist(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(del(ctx, c, c.ServiceURL("blacklists", id)), "deleting blacklist %q", id)
}

// listTLDs is the unwrapped listing FindTLDID shares.
func listTLDs(ctx context.Context, c *gophercloud.ServiceClient, name string, limit int) ([]TLD, error) {
	q, err := query(struct {
		Name string `q:"name"`
	}{name})
	if err != nil {
		return nil, err
	}
	return listAll(ctx, c, c.ServiceURL("tlds")+q, limit,
		func(raw json.RawMessage) ([]TLD, string, error) {
			var page struct {
				TLDs  []TLD `json:"tlds"`
				Links links `json:"links"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", fmt.Errorf("parsing TLD list: %w", err)
			}
			return page.TLDs, page.Links.Next, nil
		})
}

// ListTLDs lists TLDs, filtered by exact name when name is set, stopping
// after limit results (0 = no cap).
func ListTLDs(ctx context.Context, c *gophercloud.ServiceClient, name string, limit int) ([]TLD, error) {
	all, err := listTLDs(ctx, c, name, limit)
	return all, wrap(err, "listing TLDs")
}

// FindTLDID resolves a TLD name or ID to an ID.
func FindTLDID(ctx context.Context, c *gophercloud.ServiceClient, ref string) (string, error) {
	return findID(ctx, "TLD", ref, func(ctx context.Context) ([]TLD, error) {
		return listTLDs(ctx, c, ref, 0)
	}, func(t TLD) string { return t.ID })
}

// GetTLD reads one TLD.
func GetTLD(ctx context.Context, c *gophercloud.ServiceClient, id string) (*TLD, error) {
	var t TLD
	if err := getJSON(ctx, c, c.ServiceURL("tlds", id), &t); err != nil {
		return nil, wrap(err, "showing TLD %q", id)
	}
	return &t, nil
}

// CreateTLD creates a TLD; an empty description is left out.
func CreateTLD(ctx context.Context, c *gophercloud.ServiceClient, name, description string) (*TLD, error) {
	body := map[string]any{"name": name}
	if description != "" {
		body["description"] = description
	}
	var t TLD
	if err := postJSON(ctx, c, c.ServiceURL("tlds"), body, &t); err != nil {
		return nil, wrap(err, "creating TLD %q", name)
	}
	return &t, nil
}

// UpdateTLD patches a TLD with body's keys (name, description); a nil value
// clears that key.
func UpdateTLD(ctx context.Context, c *gophercloud.ServiceClient, id string, body map[string]any) (*TLD, error) {
	var t TLD
	if err := patchJSON(ctx, c, c.ServiceURL("tlds", id), body, &t); err != nil {
		return nil, wrap(err, "updating TLD %q", id)
	}
	return &t, nil
}

// DeleteTLD deletes a TLD.
func DeleteTLD(ctx context.Context, c *gophercloud.ServiceClient, id string) error {
	return wrap(del(ctx, c, c.ServiceURL("tlds", id)), "deleting TLD %q", id)
}
