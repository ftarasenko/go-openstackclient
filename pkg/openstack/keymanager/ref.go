package keymanager

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

func wrap(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}

// uuidRe matches a UUID in either spelling Python's uuid.UUID accepts from a
// barbican href: hyphenated or 32 bare hex digits.
var uuidRe = regexp.MustCompile(`^(?i:[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12})$`)

// RefUUID returns the UUID a barbican reference names: the last path element
// of an href, or the reference itself when it is a bare UUID, in lower-case
// hyphenated form. entity names the kind in the error ("Secret incorrectly
// specified"), as barbicanclient's validate_ref_and_return_uuid does.
func RefUUID(ref, entity string) (string, error) {
	ref = strings.TrimRight(ref, "/")
	last := ref[strings.LastIndex(ref, "/")+1:]
	if !uuidRe.MatchString(last) {
		return "", fmt.Errorf("%s incorrectly specified", entity)
	}
	u := strings.ToLower(strings.ReplaceAll(last, "-", ""))
	return u[0:8] + "-" + u[8:12] + "-" + u[12:16] + "-" + u[16:20] + "-" + u[20:], nil
}

// ACLTarget is the secret or container an ACL belongs to.
type ACLTarget struct {
	// Ref is the href as given, without a trailing "/"; errors name it.
	Ref string
	// ID is the secret's or container's UUID.
	ID string
	// Container is true for a container's ACL, false for a secret's.
	Container bool
}

// ParseACLRef reads the href of a secret or a container. It needs the full
// href, since a bare UUID does not say which of the two it is.
func ParseACLRef(ref string) (ACLTarget, error) {
	t := ACLTarget{Ref: strings.TrimRight(ref, "/")}
	elems := strings.Split(t.Ref, "/")
	kind := "Secret"
	switch {
	case slices.Contains(elems, "secrets"):
	case slices.Contains(elems, "containers"):
		t.Container, kind = true, "Container"
	default:
		return t, errors.New("secret or container URI is not specified: give the full href, not a bare UUID")
	}
	id, err := RefUUID(t.Ref, kind)
	t.ID = id
	return t, err
}
