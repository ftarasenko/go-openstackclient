package server

import (
	"errors"
	"fmt"
)

// relabel rewords an error from pkg/openstack/compute the way this command has
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
