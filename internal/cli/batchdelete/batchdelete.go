// Package batchdelete gives every "koc <noun> delete <ref> [<ref> ...]" seam a
// single, shared error contract: attempt every ref, never stop at the first
// failure. This matches upstream python-openstackclient, which deletes
// everything it can from a batch and reports "Failed to delete N of M" rather
// than aborting on the first bad ref.
package batchdelete

import "errors"

// Each calls del once per ref, in the order given, and never lets one failing
// call skip the rest. Failures are combined with errors.Join, so the returned
// error names every ref that failed (each del implementation is expected to
// wrap its own error with the ref, e.g. fmt.Errorf("deleting %s: %w", ref,
// err)) while the refs that succeeded stay deleted. It returns nil once every
// ref has been attempted and none failed.
func Each(refs []string, del func(ref string) error) error {
	var errs []error
	for _, ref := range refs {
		if err := del(ref); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
