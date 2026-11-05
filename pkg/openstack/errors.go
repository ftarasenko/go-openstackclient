package openstack

import "errors"

// ErrNotFound is matched, through errors.Is, by every koc error that means
// "the named resource does not exist" — for one, the strict zero-match error
// of package resolve. gophercloud's own 404s are not wrapped into it; test
// those with gophercloud.ResponseCodeIs(err, http.StatusNotFound).
var ErrNotFound = errors.New("not found")

// ErrNoObject is returned, wrapped, when a call succeeds but the reply holds
// none of the object it should, such as a quota set answered with "{}".
var ErrNoObject = errors.New("the server returned success but no object")

// ServiceError is a failure to build a service client — almost always a
// service missing from the catalog, or from the requested region. Service is
// the catalog type koc asked for.
type ServiceError struct {
	Service string
	Err     error
}

func (e *ServiceError) Error() string {
	return "creating " + e.Service + " client: " + e.Err.Error()
}

func (e *ServiceError) Unwrap() error { return e.Err }

func wrapService(name string, err error) error {
	return &ServiceError{Service: name, Err: err}
}
