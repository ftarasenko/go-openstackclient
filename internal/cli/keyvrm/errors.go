package keyvrm

import "errors"

// errNoUpdateFields is returned by "set" verbs when no updatable flag was given.
var errNoUpdateFields = errors.New("no fields to update: specify at least one flag")
