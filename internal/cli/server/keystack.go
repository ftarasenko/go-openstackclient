package server

import (
	"fmt"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
)

// keystackExtErr annotates an API error raised by a KeyStack-specific nova
// extension.
//
// Several koc compute commands drive downstream KeyStack additions to nova that
// upstream/vanilla nova does not implement: dynamic server groups
// (addServerGroup/removeServerGroup actions), the os-services admin_state /
// error_details fields, the per-instance availability_zone update, and the
// created-/deleted-* server-list filters. On a non-KeyStack cloud nova rejects
// the unknown action, body field or query parameter — with HTTP 400 from its
// request-body JSON-schema (additionalProperties: false) or 404 for an unknown
// route — so these can never silently do the wrong thing on a current cloud.
//
// This wraps that failure with a clear pointer to the required extension instead
// of surfacing a raw gophercloud response dump. gophercloud.ResponseCodeIs
// unwraps the error chain, so callers may wrap with additional %w context first.
func keystackExtErr(err error, feature string) error {
	if err == nil {
		return nil
	}
	if gophercloud.ResponseCodeIs(err, http.StatusBadRequest) ||
		gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
		return fmt.Errorf("%w\n\nnote: this uses the KeyStack %s nova extension, which the target cloud rejected; confirm you are pointed at a KeyStack deployment", err, feature)
	}
	return err
}
