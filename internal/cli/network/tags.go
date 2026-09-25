package network

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/attributestags"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Literals repeated within this file.
const (
	repeatableSuffix = " (repeatable)"
)

// Neutron tags are a sub-resource (PUT /v2.0/<collection>/<id>/tags), never a
// body attribute: a create cannot carry them and an update ignores them. So
// every tag-aware write verb is "write the resource, then replace its tag set",
// exactly as upstream does it (osc_lib/utils/tags.py update_tags_for_set /
// update_tags_for_unset). The helpers below are that shape, once.
//
// The collection names passed as resourceType are neutron's URL segments.
const (
	tagResourceNetworks       = "networks"
	tagResourceSubnets        = "subnets"
	tagResourcePorts          = "ports"
	tagResourceRouters        = "routers"
	tagResourceFloatingIPs    = "floatingips"
	tagResourceSecurityGroups = "security-groups"
	tagResourceSubnetPools    = "subnetpools"
)

const (
	flagTag   = "tag"
	flagNoTag = "no-tag"
	// flagAllTag is upstream's spelling on unset verbs (not "--all-tags").
	flagAllTag = "all-tag"
)

// tagFilterFlags are the four list filters upstream adds with
// add_tag_filtering_option_to_parser. Each is a comma-separated list, sent to
// neutron joined with commas under tags / tags-any / not-tags / not-tags-any —
// the fields every vendored ListOpts spells Tags/TagsAny/NotTags/NotTagsAny.
type tagFilterFlags struct {
	tags       []string
	anyTags    []string
	notTags    []string
	notAnyTags []string
}

// bindTagFilterFlags registers --tags, --any-tags, --not-tags and
// --not-any-tags. plural names the listed resource ("networks") in the help.
func bindTagFilterFlags(fl *pflag.FlagSet, f *tagFilterFlags, plural string) {
	fl.StringSliceVar(&f.tags, "tags", nil, "list only "+plural+" with all of these tags (comma-separated)")
	fl.StringSliceVar(&f.anyTags, "any-tags", nil, "list only "+plural+" with any of these tags (comma-separated)")
	fl.StringSliceVar(&f.notTags, "not-tags", nil, "exclude "+plural+" with all of these tags (comma-separated)")
	fl.StringSliceVar(&f.notAnyTags, "not-any-tags", nil, "exclude "+plural+" with any of these tags (comma-separated)")
}

// apply copies the filters into a ListOpts' four tag fields.
func (f *tagFilterFlags) apply(tags, anyTags, notTags, notAnyTags *string) {
	*tags = strings.Join(f.tags, ",")
	*anyTags = strings.Join(f.anyTags, ",")
	*notTags = strings.Join(f.notTags, ",")
	*notAnyTags = strings.Join(f.notAnyTags, ",")
}

// tagWriteFlags carries the write-side tag flags: --tag (repeatable) plus
// --no-tag on create/set, or --all-tag on unset.
type tagWriteFlags struct {
	tags   []string
	noTag  bool
	allTag bool
}

// bindTagCreateFlags registers upstream's create pair: --tag (repeatable) and
// --no-tag, mutually exclusive (add_tag_option_to_parser_for_create).
func bindTagCreateFlags(cmd *cobra.Command, f *tagWriteFlags, noun string) {
	fl := cmd.Flags()
	fl.StringArrayVar(&f.tags, flagTag, nil, "tag to add to the "+noun+repeatableSuffix)
	fl.BoolVar(&f.noTag, flagNoTag, false, "create the "+noun+" with no tags")
	cmd.MarkFlagsMutuallyExclusive(flagTag, flagNoTag)
}

// bindTagSetFlags registers upstream's set pair. Unlike create they combine:
// --no-tag clears the current tags first, so "--no-tag --tag x" overwrites
// the whole set with {x} (add_tag_option_to_parser_for_set).
func bindTagSetFlags(fl *pflag.FlagSet, f *tagWriteFlags, noun string) {
	fl.StringArrayVar(&f.tags, flagTag, nil, "tag to add to the "+noun+repeatableSuffix)
	fl.BoolVar(&f.noTag, flagNoTag, false,
		"clear the "+noun+"'s tags; combine with --tag to overwrite them")
}

// bindTagUnsetFlags registers upstream's unset pair: --tag (repeatable) and
// --all-tag, mutually exclusive (add_tag_option_to_parser_for_unset).
func bindTagUnsetFlags(cmd *cobra.Command, f *tagWriteFlags, noun string) {
	fl := cmd.Flags()
	fl.StringArrayVar(&f.tags, flagTag, nil, "tag to remove from the "+noun+repeatableSuffix)
	fl.BoolVar(&f.allTag, flagAllTag, false, "remove every tag from the "+noun)
	cmd.MarkFlagsMutuallyExclusive(flagTag, flagAllTag)
}

// given reports whether any write-side tag flag was used, so a set/unset verb
// can count tags as "an attribute was given" and skip the resource PUT when
// tags are the only change — upstream skips it too.
func (f *tagWriteFlags) given() bool {
	return len(f.tags) > 0 || f.noTag || f.allTag
}

// forSet computes the tag set a create/set verb leaves behind, from the
// resource's current tags: --no-tag starts from empty, then --tag adds.
func (f *tagWriteFlags) forSet(current []string) []string {
	var next []string
	if !f.noTag {
		next = append(next, current...)
	}
	next = append(next, f.tags...)
	return normaliseTags(next)
}

// forUnset computes the tag set an unset verb leaves behind: --all-tag clears,
// --tag removes each named tag.
func (f *tagWriteFlags) forUnset(current []string) []string {
	if f.allTag {
		return []string{}
	}
	return normaliseTags(keepUnmatched(current, func(t string) bool { return slices.Contains(f.tags, t) }))
}

// normaliseTags deduplicates and sorts, which is the order neutron and
// upstream (sorted(list(tags))) both use.
func normaliseTags(tags []string) []string {
	out := slices.Clone(tags)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// replaceTags writes next as the resource's whole tag set when it differs from
// current, and returns the tag set the resource now carries. An unchanged set
// sends nothing, matching upstream's "if set(obj.tags) != tags" guard.
func replaceTags(ctx context.Context, client *gophercloud.ServiceClient, resourceType, id string, current, next []string) ([]string, error) {
	if slices.Equal(normaliseTags(current), next) {
		return current, nil
	}
	got, err := attributestags.ReplaceAll(ctx, client, resourceType, id,
		attributestags.ReplaceAllOpts{Tags: next}).Extract()
	if err != nil {
		return nil, fmt.Errorf("setting tags on %s %s: %w", resourceType, id, err)
	}
	return got, nil
}

// tagApplier is applyTagsForSet or applyTagsForUnset.
type tagApplier func(context.Context, *gophercloud.ServiceClient, string, string, []string, *tagWriteFlags) ([]string, error)

// tagEdit is the tag half of a set/unset tail: the helper to apply and the
// flags it reads.
type tagEdit struct {
	apply tagApplier
	flags *tagWriteFlags
}

func (t tagEdit) run(ctx context.Context, client *gophercloud.ServiceClient, resourceType, id string, current []string) ([]string, error) {
	return t.apply(ctx, client, resourceType, id, current, t.flags)
}

// applyTagsForSet is replaceTags(forSet) — the tail of every create/set verb.
// It is a no-op returning current when no tag flag was given.
func applyTagsForSet(ctx context.Context, client *gophercloud.ServiceClient, resourceType, id string, current []string, f *tagWriteFlags) ([]string, error) {
	if !f.given() {
		return current, nil
	}
	return replaceTags(ctx, client, resourceType, id, current, f.forSet(current))
}

// applyTagsForUnset is replaceTags(forUnset) — the tail of every unset verb.
func applyTagsForUnset(ctx context.Context, client *gophercloud.ServiceClient, resourceType, id string, current []string, f *tagWriteFlags) ([]string, error) {
	if !f.given() {
		return current, nil
	}
	return replaceTags(ctx, client, resourceType, id, current, f.forUnset(current))
}
