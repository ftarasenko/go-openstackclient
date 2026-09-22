// Package extract guards gophercloud's single-object Extract helpers.
//
// Every one of them is shaped like
//
//	var s struct{ QuotaSet *QuotaSet `json:"quota_set"` }
//	err := r.ExtractInto(&s)
//	return s.QuotaSet, err
//
// so a 2xx response whose body does not carry the wrapper key yields
// (nil, nil): no error, and a pointer the caller then dereferences. koc's
// commands pass that pointer straight to a field-rendering helper, so the
// answer to a well-formed request is a segmentation fault rather than a
// message. It is reachable from anything that returns 200 without the body the
// service is supposed to send — a proxy, a gateway error page served as JSON,
// an API that changed shape.
package extract

import "errors"

// errNoObject is deliberately plain: every call site already wraps the result
// with what it was reading ("getting compute limits: …"), and a second noun
// here would only repeat it.
var errNoObject = errors.New("the server returned success but no object")

// One turns a gophercloud single-object Extract into one that cannot hand back
// a nil pointer with a nil error. Wrap the result the way the call site already
// wraps a failure:
//
//	cl, err := extract.One(computelimits.Get(ctx, sc, opts).Extract())
//	if err != nil {
//		return fmt.Errorf("getting compute limits: %w", err)
//	}
func One[T any](v *T, err error) (*T, error) {
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, errNoObject
	}
	return v, nil
}
