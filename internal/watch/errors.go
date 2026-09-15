package watch

import (
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"

	"github.com/gophercloud/gophercloud/v2"

	"github.com/ftarasenko/go-openstackclient/internal/s3"
)

// A watched command fails in two very different ways, and the loop has to tell
// them apart or it is useless.
//
// A fleet-wide `server list --all` crossing every cell picks up a 503 or a
// reset connection often enough that exiting on one would make --watch a
// worse tool than the `watch -n1` it replaces; those failures must leave the
// last good frame on screen and be forgotten on the next tick. A rejected
// credential is the opposite: retrying it every second hammers Keystone and, on
// a cloud with lockout configured, is how an operator locks their own account
// out while staring at the screen that is doing it.

// reauthFailure returns the error gophercloud produces when a token expired,
// it went back to Keystone for a new one, and Keystone said no.
//
// It has to be reached for explicitly. ErrUnableToReauthenticate deliberately
// does not Unwrap — its comment says ErrOriginal and ErrReauth are independent
// failures — so errors.As finds no status code inside it and every rule below
// that keys on one silently declines to fire. Measured before this existed: a
// watch whose credential Keystone had started refusing ran all twenty of its
// refreshes, each one a fresh rejected login.
func reauthFailure(err error) (*gophercloud.ErrUnableToReauthenticate, bool) {
	var unable *gophercloud.ErrUnableToReauthenticate
	if errors.As(err, &unable) {
		return unable, true
	}
	return nil, false
}

// httpStatus reports the HTTP status an error carries, if any. It covers both
// error shapes koc produces: gophercloud's for every OpenStack service, and
// internal/s3's for the hand-rolled S3 client, which has no gophercloud
// underneath it.
func httpStatus(err error) (int, bool) {
	var unexpected gophercloud.ErrUnexpectedResponseCode
	if errors.As(err, &unexpected) {
		return unexpected.Actual, true
	}
	var apiErr *s3.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode, true
	}
	return 0, false
}

// isAuthFailure reports whether err is the endpoint refusing the credential
// rather than the request. Such a failure is fatal to the loop whatever
// --watch-errors says: a second attempt with the same token cannot succeed, and
// a thousand of them is an attack on the operator's own account.
func isAuthFailure(err error) bool {
	if unable, ok := reauthFailure(err); ok {
		// Keystone refused a new token. That is the credential being rejected —
		// the password changed, the account was disabled, the application
		// credential was revoked — unless Keystone itself was what failed, in
		// which case the credential may well still be good and the loop should
		// ride it out.
		return !isTransient(unable.ErrReauth)
	}
	code, ok := httpStatus(err)
	if !ok {
		return false
	}
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

// isTransient reports whether err is the kind that comes and goes, so that a
// refresh which failed has a reason to be tried again.
//
// It is consulted only for the *first* refresh, to decide whether a loop that
// has never rendered anything should keep going. Once a frame has rendered,
// every non-auth failure is tolerated: there is a last good frame to hold, and
// an operator watching a fleet through a rolling restart wants exactly that.
func isTransient(err error) bool {
	// A failed re-authentication is as transient as whatever stopped Keystone
	// answering, which is the one thing inside it that says so.
	if unable, ok := reauthFailure(err); ok {
		return isTransient(unable.ErrReauth)
	}
	if code, ok := httpStatus(err); ok {
		switch {
		case code >= http.StatusInternalServerError:
			return true
		case code == http.StatusRequestTimeout, code == http.StatusTooManyRequests:
			return true
		default:
			return false
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	switch {
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNABORTED):
		return true
	case errors.Is(err, syscall.EPIPE), errors.Is(err, syscall.ECONNREFUSED):
		return true
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return true
	case errors.Is(err, net.ErrClosed):
		return true
	}
	// Not an answer from any endpoint and not a recognisable network fault: a
	// rejected column name, an unparseable filter, a service missing from the
	// catalog. Those are settled, not transient.
	return false
}
