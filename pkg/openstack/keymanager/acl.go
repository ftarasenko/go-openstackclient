package keymanager

import (
	"context"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/acls"
)

// GetACL returns the ACL of a secret or container; an entity with none
// returns an empty ACL.
func GetACL(ctx context.Context, c *gophercloud.ServiceClient, t ACLTarget) (acls.ACL, error) {
	var r acls.ACLResult
	if t.Container {
		r = acls.GetContainerACL(ctx, c, t.ID)
	} else {
		r = acls.GetSecretACL(ctx, c, t.ID)
	}
	acl, err := r.Extract()
	if err != nil {
		return nil, wrap(err, "getting ACL of %s", t.Ref)
	}
	if acl == nil {
		return acls.ACL{}, nil
	}
	return *acl, nil
}

// SetACL replaces the ACL of a secret or container with opts.
func SetACL(ctx context.Context, c *gophercloud.ServiceClient, t ACLTarget, opts acls.SetOpts) error {
	var err error
	if t.Container {
		_, err = acls.SetContainerACL(ctx, c, t.ID, opts).Extract()
	} else {
		_, err = acls.SetSecretACL(ctx, c, t.ID, opts).Extract()
	}
	return wrap(err, "setting ACL of %s", t.Ref)
}

// DeleteACL deletes the ACL of a secret or container, which restores the
// default: the project has access, and no user is listed.
func DeleteACL(ctx context.Context, c *gophercloud.ServiceClient, t ACLTarget) error {
	var err error
	if t.Container {
		err = acls.DeleteContainerACL(ctx, c, t.ID).ExtractErr()
	} else {
		err = acls.DeleteSecretACL(ctx, c, t.ID).ExtractErr()
	}
	return wrap(err, "deleting ACL of %s", t.Ref)
}

// ACLChange is a change to one operation's entry of an ACL.
type ACLChange struct {
	// OperationType is the operation the entry governs; barbican knows only
	// "read".
	OperationType string
	Users         []string
	// ProjectAccess, when set, sets the entry's project-access flag.
	ProjectAccess *bool
}

// SubmitACLOpts replaces the whole ACL with one entry: ch's users and flag.
func SubmitACLOpts(ch ACLChange) acls.SetOpts {
	users := append([]string{}, ch.Users...)
	return acls.SetOpts{{Type: ch.OperationType, Users: &users, ProjectAccess: ch.ProjectAccess}}
}

// currentOpts restates an ACL as the SetOpts that would write it back, in
// operation order.
func currentOpts(current acls.ACL) acls.SetOpts {
	ops := make([]string, 0, len(current))
	for op := range current {
		ops = append(ops, op)
	}
	slices.Sort(ops)
	opts := make(acls.SetOpts, 0, len(ops))
	for _, op := range ops {
		d := current[op]
		users := append([]string{}, d.Users...)
		pa := d.ProjectAccess
		opts = append(opts, acls.SetOpt{Type: op, Users: &users, ProjectAccess: &pa})
	}
	return opts
}

// AddACLUsersOpts writes back current with ch's users added to its
// operation's entry (creating the entry if there is none) and ch's flag, if
// set, applied to it.
func AddACLUsersOpts(current acls.ACL, ch ACLChange) acls.SetOpts {
	opts := currentOpts(current)
	for i := range opts {
		if opts[i].Type != ch.OperationType {
			continue
		}
		for _, u := range ch.Users {
			if !slices.Contains(*opts[i].Users, u) {
				*opts[i].Users = append(*opts[i].Users, u)
			}
		}
		if ch.ProjectAccess != nil {
			opts[i].ProjectAccess = ch.ProjectAccess
		}
		return opts
	}
	users := append([]string{}, ch.Users...)
	return append(opts, acls.SetOpt{Type: ch.OperationType, Users: &users, ProjectAccess: ch.ProjectAccess})
}

// RemoveACLUsersOpts writes back current with ch's users removed from its
// operation's entry and ch's flag, if set, applied to it.
func RemoveACLUsersOpts(current acls.ACL, ch ACLChange) acls.SetOpts {
	opts := currentOpts(current)
	for i := range opts {
		if opts[i].Type != ch.OperationType {
			continue
		}
		kept := slices.DeleteFunc(*opts[i].Users, func(u string) bool { return slices.Contains(ch.Users, u) })
		opts[i].Users = &kept
		if ch.ProjectAccess != nil {
			opts[i].ProjectAccess = ch.ProjectAccess
		}
	}
	return opts
}

// AddACLUsers reads the ACL of t and writes it back with AddACLUsersOpts.
func AddACLUsers(ctx context.Context, c *gophercloud.ServiceClient, t ACLTarget, ch ACLChange) error {
	current, err := GetACL(ctx, c, t)
	if err != nil {
		return err
	}
	return SetACL(ctx, c, t, AddACLUsersOpts(current, ch))
}

// RemoveACLUsers reads the ACL of t and writes it back with
// RemoveACLUsersOpts.
func RemoveACLUsers(ctx context.Context, c *gophercloud.ServiceClient, t ACLTarget, ch ACLChange) error {
	current, err := GetACL(ctx, c, t)
	if err != nil {
		return err
	}
	return SetACL(ctx, c, t, RemoveACLUsersOpts(current, ch))
}
