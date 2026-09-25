package network

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
)

// flagExtraProperty is upstream's escape hatch for resource attributes the CLI
// does not model (network/common.py NeutronCommandWithExtraArgs):
//
//	--extra-property type=<type>,name=<name>,value=<value>
//
// The attribute is sent as given, as a top-level key of the resource body.
const flagExtraProperty = "extra-property"

const extraPropertyHelp = "extra attribute to send as type=<type>,name=<name>,value=<value> (repeatable); " +
	"type is str (default), int, bool, list (';'-separated) or dict (';'-separated key:value pairs)"

const extraPropertyUnsetHelp = "attribute to clear, as name=<name> (repeatable; sent as null)"

// bindExtraPropertyFlag registers --extra-property on a create or set verb.
func bindExtraPropertyFlag(fl *pflag.FlagSet, specs *[]string) {
	fl.StringArrayVar(specs, flagExtraProperty, nil, extraPropertyHelp)
}

// bindExtraPropertyUnsetFlag registers --extra-property on an unset verb. Upstream
// uses the same parser there (name and value required) but sends every named
// attribute as None, so the value is accepted and ignored.
func bindExtraPropertyUnsetFlag(fl *pflag.FlagSet, specs *[]string) {
	fl.StringArrayVar(specs, flagExtraProperty, nil, extraPropertyUnsetHelp)
}

// parseExtraProperties turns --extra-property specs into body attributes, using
// upstream's converters (network/utils.py str2bool/str2list/str2dict). With
// unset, every named attribute maps to nil (JSON null), as upstream's
// NeutronUnsetCommandWithExtraArgs does; only name= is required there.
func parseExtraProperties(specs []string, unset bool) (map[string]any, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(specs))
	for _, spec := range specs {
		kv, err := parseExtraPropertySpec(spec)
		if err != nil {
			return nil, err
		}
		name, ok := kv["name"]
		if !ok || name == "" {
			return nil, fmt.Errorf("--extra-property %q requires name=", spec)
		}
		if unset {
			out[name] = nil
			continue
		}
		value, ok := kv["value"]
		if !ok {
			return nil, fmt.Errorf("--extra-property %q requires value=", spec)
		}
		v, err := convertExtraProperty(kv["type"], value)
		if err != nil {
			return nil, fmt.Errorf("--extra-property %q: %w", spec, err)
		}
		out[name] = v
	}
	return out, nil
}

// parseExtraPropertySpec splits "type=…,name=…,value=…" on commas and the first
// '=' of each pair, rejecting unknown keys — osc-lib's MultiKeyValueAction.
func parseExtraPropertySpec(spec string) (map[string]string, error) {
	kv := map[string]string{}
	for _, part := range strings.Split(spec, ",") {
		k, v, err := splitKV(part)
		if err != nil {
			return nil, fmt.Errorf("parsing --extra-property %q: %w", spec, err)
		}
		switch k {
		case "type", "name", "value":
			kv[k] = v
		default:
			return nil, fmt.Errorf("parsing --extra-property %q: unknown key %q (want type, name, value)", spec, k)
		}
	}
	return kv, nil
}

func convertExtraProperty(typ, value string) (any, error) {
	switch typ {
	case "", "str":
		return value, nil
	case "int":
		n, err := strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("value %q is not an int", value)
		}
		return n, nil
	case "bool":
		// str2bool: anything but a case-insensitive "true" is false.
		return strings.EqualFold(value, "true"), nil
	case "list":
		if value == "" {
			return []string{}, nil
		}
		return strings.Split(value, ";"), nil
	case "dict":
		return parseExtraPropertyDict(value)
	default:
		return nil, fmt.Errorf("type %q is not supported (want str, int, bool, list or dict)", typ)
	}
}

// parseExtraPropertyDict is str2dict: "k1:v1;k2:v2", where a ';'-separated
// piece with no ':' belongs to the previous value.
func parseExtraPropertyDict(value string) (map[string]string, error) {
	out := map[string]string{}
	if value == "" {
		return out, nil
	}
	var pairs []string
	for _, piece := range strings.Split(value, ";") {
		switch {
		case strings.Contains(piece, ":"):
			pairs = append(pairs, piece)
		case len(pairs) == 0:
			return nil, fmt.Errorf("missing value for key %q", piece)
		default:
			pairs[len(pairs)-1] += ";" + piece
		}
	}
	for _, p := range pairs {
		k, v, _ := strings.Cut(p, ":")
		out[k] = v
	}
	return out, nil
}

// bodyExt layers extra top-level attributes onto a gophercloud request body.
// gophercloud's typed opts model only part of what neutron accepts (extension
// attributes such as qos_policy_id, dns_domain or port_security_enabled), so
// every write verb composes its opts with this rather than growing one
// hand-written wrapper per missing key. build is the wrapped builder's own
// To<Resource><Verb>Map method and key the body's envelope ("network",
// "port", …). Extra attributes win over the typed ones, as upstream's
// attrs.update(extra_properties) does.
type bodyExt struct {
	build func() (map[string]any, error)
	key   string
	extra map[string]any
}

func (b bodyExt) body() (map[string]any, error) {
	base, err := b.build()
	if err != nil || len(b.extra) == 0 {
		return base, err
	}
	inner, ok := base[b.key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected %s request body shape: %T", b.key, base[b.key])
	}
	for k, v := range b.extra {
		inner[k] = v
	}
	return base, nil
}

// mergeAttrs copies src into dst, allocating dst when needed, and returns it.
// It lets a verb collect flag-derived attributes and --extra-property ones into
// one map, the latter last so they win.
func mergeAttrs(dst, src map[string]any) map[string]any {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]any, len(src))
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
