package compute

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
)

// gophercloud v2 has no package for os-instance-actions, so these are raw
// requests decoding into the types below.

// InstanceAction is one action taken on a server (create, reboot, resize,
// ...). updated_at appears at 2.58.
type InstanceAction struct {
	Action       string `json:"action"`
	InstanceUUID string `json:"instance_uuid"`
	// Message is set when the action failed.
	Message   string `json:"message"`
	ProjectID string `json:"project_id"`
	RequestID string `json:"request_id"`
	StartTime string `json:"start_time"`
	UpdatedAt string `json:"updated_at"`
	UserID    string `json:"user_id"`
}

// InstanceActionListOpts filters [ListInstanceActions]. Every field is
// optional.
type InstanceActionListOpts struct {
	// Marker is the request ID to list after (2.58+).
	Marker string
	// ChangesSince needs 2.58, ChangesBefore 2.66 (ISO 8601).
	ChangesSince  string
	ChangesBefore string
}

// ListInstanceActions lists a server's actions, newest first. limit > 0 is
// sent as nova's page size and caps the result; no further page is read.
func ListInstanceActions(ctx context.Context, c *gophercloud.ServiceClient, serverID string,
	opts InstanceActionListOpts, limit int,
) ([]InstanceAction, error) {
	vals := url.Values{}
	for key, val := range map[string]string{
		"marker":         opts.Marker,
		"changes-since":  opts.ChangesSince,
		"changes-before": opts.ChangesBefore,
	} {
		if val != "" {
			vals.Set(key, val)
		}
	}
	if limit > 0 {
		vals.Set("limit", strconv.Itoa(limit))
	}
	u := c.ServiceURL("servers", serverID, "os-instance-actions")
	if q := vals.Encode(); q != "" {
		u += "?" + q
	}
	var resp struct {
		InstanceActions []InstanceAction `json:"instanceActions"`
	}
	if err := getJSON(ctx, c, u, &resp); err != nil {
		return nil, fmt.Errorf("listing events of server %s: %w", serverID, err)
	}
	all := resp.InstanceActions
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// InstanceActionEvent is one step of an action. Result is "Success" or
// "Error" once the step has finished and empty while it runs; Traceback is
// admin-only.
type InstanceActionEvent struct {
	Event      string `json:"event"`
	StartTime  string `json:"start_time"`
	FinishTime string `json:"finish_time"`
	Result     string `json:"result"`
	Traceback  string `json:"traceback"`
	Host       string `json:"host"`
	HostID     string `json:"hostId"`
	Details    string `json:"details"`
}

// GetInstanceAction reads one action of a server, with its events, as nova
// sent it.
func GetInstanceAction(ctx context.Context, c *gophercloud.ServiceClient, serverID, requestID string) (map[string]any, error) {
	var resp struct {
		InstanceAction map[string]any `json:"instanceAction"`
	}
	if err := getJSON(ctx, c, c.ServiceURL("servers", serverID, "os-instance-actions", requestID), &resp); err != nil {
		return nil, fmt.Errorf("showing event %s of server %s: %w", requestID, serverID, err)
	}
	return resp.InstanceAction, nil
}

// ListInstanceActionEvents reads the steps of one action of a server.
func ListInstanceActionEvents(ctx context.Context, c *gophercloud.ServiceClient, serverID, requestID string) ([]InstanceActionEvent, error) {
	var resp struct {
		InstanceAction struct {
			Events []InstanceActionEvent `json:"events"`
		} `json:"instanceAction"`
	}
	if err := getJSON(ctx, c, c.ServiceURL("servers", serverID, "os-instance-actions", requestID), &resp); err != nil {
		return nil, fmt.Errorf("showing event %s of server %s: %w", requestID, serverID, err)
	}
	return resp.InstanceAction.Events, nil
}
