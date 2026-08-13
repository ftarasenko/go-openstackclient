// Package nameflag reconciles a resource name supplied as a positional argument
// with one supplied as --name.
//
// Upstream `openstack` is inconsistent about which form a create verb takes.
// python-designateclient's `tsigkey create` and every python-octaviaclient
// create verb name the new resource with a **--name flag** and have no
// positional for it (`loadbalancer create --name lb1`), while koc originally
// modelled those as `create <name>` — the shape most of the OSC surface uses.
// A script written against `openstack` therefore failed with "unknown flag:
// --name", and one written against koc would fail against a client that only
// accepts the flag.
//
// Rather than pick a winner, the affected commands accept both: the positional
// becomes optional and --name is registered alongside it. This package holds the
// one rule they share so the reconciliation is not re-implemented per command.
package nameflag

import "fmt"

// Resolve returns the name a create verb should use, given the leftover
// positional arguments that may carry it and the value of --name.
//
// At most one positional is expected in args (pass the slice already trimmed of
// any earlier positional the command takes, e.g. the pool for
// `loadbalancer member create <pool> [<name>]`). Supplying the name both ways is
// accepted only when the two agree, so a script that belts-and-braces both forms
// works while a genuine mix-up is reported instead of one value silently
// winning. When required is false an absent name yields "", which is what the
// API treats as "no name" — upstream's default for most octavia resources.
func Resolve(args []string, name string, required bool) (string, error) {
	positional := ""
	if len(args) > 0 {
		positional = args[0]
	}
	switch {
	case positional != "" && name != "" && positional != name:
		return "", fmt.Errorf("name given twice with different values (%q as an argument, %q via --name)", positional, name)
	case positional != "":
		return positional, nil
	case name != "":
		return name, nil
	case required:
		return "", fmt.Errorf("a name is required: pass it as an argument or with --name")
	default:
		return "", nil
	}
}
