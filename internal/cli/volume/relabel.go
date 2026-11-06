package volume

import (
	"context"
	"errors"
	"fmt"

	volumeapi "github.com/ftarasenko/koc/pkg/openstack/volume"
)

// relabel rewords an error from pkg/openstack/volume the way this command has
// always printed it. The public functions name a resource by the ID they were
// given; these verbs name it by what the operator typed, so they keep their own
// wording around the same underlying error.
func relabel(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	inner := errors.Unwrap(err)
	if inner == nil {
		return err
	}
	return fmt.Errorf(format+": %w", append(args, inner)...)
}

// inner strips the pkg wrap from err, for a caller that adds its own.
func inner(err error) error {
	if u := errors.Unwrap(err); u != nil {
		return u
	}
	return err
}

// relabelWait rewords a pkg wait's error: a timeout, a failed read, or the
// volume settling in a state the wait did not want (failed()).
func relabelWait(err error, ref string, failed func(*volumeapi.WaitFailedError) error) error {
	var wf *volumeapi.WaitFailedError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &wf):
		return failed(wf)
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return relabel(err, "waiting for volume %q", ref)
	default:
		return relabel(err, "polling volume %q", ref)
	}
}
