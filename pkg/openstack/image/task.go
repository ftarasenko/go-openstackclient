package image

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/tasks"

	"github.com/ftarasenko/go-openstackclient/pkg/openstack/paging"
)

// TaskListOpts filters a task listing: gophercloud's ListOpts, plus the type
// filter it declares with a json tag instead of a query one — so its query
// builder drops it, and a type filter set there is silently ignored.
type TaskListOpts struct {
	tasks.ListOpts
	// Type limits the listing to tasks of this type, e.g. "import".
	Type string
}

// ToTaskListQuery builds the query string; it implements
// tasks.ListOptsBuilder.
func (q TaskListOpts) ToTaskListQuery() (string, error) {
	s, err := q.ListOpts.ToTaskListQuery()
	if err != nil {
		return "", err
	}
	if q.Type == "" {
		return s, nil
	}
	sep := "?"
	if len(s) > 0 && s[0] == '?' {
		sep = "&"
	}
	return s + sep + "type=" + q.Type, nil
}

// ListTasks lists glance tasks. A positive opts.Limit caps the result as well
// as sizing the pages: paging stops as soon as that many tasks are in hand.
func ListTasks(ctx context.Context, c *gophercloud.ServiceClient, opts TaskListOpts) ([]tasks.Task, error) {
	all, err := paging.Collect(ctx, tasks.List(c, opts), opts.Limit, tasks.ExtractTasks)
	if err != nil {
		return nil, fmt.Errorf("listing glance tasks: %w", err)
	}
	return all, nil
}

// GetTask reads one glance task.
func GetTask(ctx context.Context, c *gophercloud.ServiceClient, id string) (*tasks.Task, error) {
	task, err := tasks.Get(ctx, c, id).Extract()
	if err != nil {
		return nil, fmt.Errorf("showing glance task %s: %w", id, err)
	}
	return task, nil
}

// Store is one backend store of a multi-store glance.
type Store struct {
	ID          string
	Description string
	Default     bool
	ReadOnly    bool
	// Properties is set only by a detailed listing.
	Properties map[string]any
}

// UnmarshalJSON decodes glance's store document, whose flags are JSON
// *strings* ({"default": "true"}) rather than booleans; both spellings are
// accepted, since the quoting is glance's inconsistency, not a contract.
func (s *Store) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID          string         `json:"id"`
		Description string         `json:"description"`
		Default     jsonBool       `json:"default"`
		ReadOnly    jsonBool       `json:"read-only"`
		Properties  map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = Store{ID: raw.ID, Description: raw.Description, Default: bool(raw.Default), ReadOnly: bool(raw.ReadOnly), Properties: raw.Properties}
	return nil
}

// jsonBool is a boolean that glance may spell as a quoted string.
type jsonBool bool

func (b *jsonBool) UnmarshalJSON(data []byte) error {
	var asBool bool
	if err := json.Unmarshal(data, &asBool); err == nil {
		*b = jsonBool(asBool)
		return nil
	}
	var asString string
	if err := json.Unmarshal(data, &asString); err != nil {
		return fmt.Errorf("expected a boolean or a quoted boolean, got %s", data)
	}
	if asString == "" {
		*b = false
		return nil
	}
	parsed, err := strconv.ParseBool(asString)
	if err != nil {
		return fmt.Errorf("expected a quoted boolean, got %q", asString)
	}
	*b = jsonBool(parsed)
	return nil
}

// ListStores reads glance's stores document (/v2/info/stores, or
// /v2/info/stores/detail with each store's properties — admin only, glance
// 2.15 or later). gophercloud has no package for it, so this is a raw GET.
//
// The endpoint exists only when multi-store is enabled; otherwise glance
// answers 404, and the error says so (gophercloud.ResponseCodeIs(err, 404)
// still holds).
func ListStores(ctx context.Context, c *gophercloud.ServiceClient, detail bool) ([]Store, error) {
	var doc struct {
		Stores []Store `json:"stores"`
	}
	url := c.ServiceURL("info", "stores")
	if detail {
		url += "/detail"
	}
	resp, err := c.Get(ctx, url, &doc, &gophercloud.RequestOpts{OkCodes: []int{http.StatusOK}})
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		if gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
			return nil, fmt.Errorf("this glance has no multi-store support enabled: %w", err)
		}
		return nil, fmt.Errorf("listing glance stores: %w", err)
	}
	return doc.Stores, nil
}
