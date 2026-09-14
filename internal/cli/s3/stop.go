package s3cli

import "errors"

// errStopListing ends a ListObjectsFunc walk early without making the caller
// treat it as a failure. The client propagates a callback error verbatim, which
// is exactly what a "stop here" signal needs — and what keeps a --limit from
// paging through the rest of a million-key bucket.
var errStopListing = errors.New("stop listing")

func isStopListing(err error) bool { return errors.Is(err, errStopListing) }
