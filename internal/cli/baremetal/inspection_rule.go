package baremetal

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/cli/batchdelete"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Inspection rules are ironic's own (API 1.96, /v1/inspection_rules), the
// replacement for ironic-inspector's rules that `koc baremetal introspection`
// still drives on older clouds. gophercloud has no package for them, so the
// verbs use the raw helpers in raw.go.
//
// Flag names follow python-ironicclient (`openstack baremetal inspection
// rule`). UNVERIFIED against KeyStack (docs.keystack.ru unreachable, HTTP 403).

var featureInspectionRule = ironicFeature{"baremetal inspection rule", "1.96", releaseEpoxy}

// inspectionRuleDetailFields are python-ironicclient's
// INSPECTION_RULE_DETAILED_RESOURCE; inspectionRuleFields the short list's.
var (
	inspectionRuleDetailFields = []string{
		"uuid", "description", "priority", "sensitive", "phase", "actions", "conditions",
		"created_at", "updated_at",
	}
	inspectionRuleFields  = []string{"uuid", "description"}
	inspectionRuleColumns = knownColumns(inspectionRuleDetailFields)
)

// newInspectionCommand builds "baremetal inspection ...", whose only noun is
// "rule".
func newInspectionCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspection",
		Short: "Ironic's built-in inspection (API 1.96, OpenStack 2025.1)",
	}
	rule := &cobra.Command{
		Use:   "rule",
		Short: "Manage ironic inspection rules (requires ironic API 1.96, OpenStack 2025.1)",
	}
	rule.AddCommand(
		newInspectionRuleListCommand(a, o),
		newInspectionRuleShowCommand(a, o),
		newInspectionRuleCreateCommand(a, o),
		newInspectionRuleSetCommand(a, o),
		newInspectionRuleUnsetCommand(a, o),
		newInspectionRuleDeleteCommand(a, o),
	)
	cmd.AddCommand(rule)
	return cmd
}

// inspectionRuleErr names the 1.96 requirement when an older cloud refuses.
func inspectionRuleErr(ctx context.Context, client *gophercloud.ServiceClient, what string, err error) error {
	return fmt.Errorf("%s: %w", what, explainMicroversion(ctx, client, featureInspectionRule, err))
}

type inspectionRuleListFlags struct {
	long    bool
	limit   int
	marker  string
	sortKey string
	sortDir string
}

func newInspectionRuleListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &inspectionRuleListFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List ironic inspection rules",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runInspectionRuleList(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.long, "long", false, "list additional fields in output")
	fl.IntVar(&f.limit, "limit", 0, "maximum number of rules to return")
	fl.StringVar(&f.marker, "marker", "", "UUID of the last rule from the previous page")
	fl.StringVar(&f.sortKey, "sort-key", "", "sort output by this rule attribute")
	fl.StringVar(&f.sortDir, "sort-dir", "", "sort direction: asc or desc")
	addListFieldsAliases(cmd, o)
	return cmd
}

func runInspectionRuleList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *inspectionRuleListFlags, w io.Writer,
) error {
	q := url.Values{"marker": {f.marker}, "sort_key": {f.sortKey}, "sort_dir": {f.sortDir}}
	fields := inspectionRuleFields
	if f.long || len(o.SelectedColumns(labels(inspectionRuleDetailFields)...)) > len(o.SelectedColumns(labels(inspectionRuleFields)...)) {
		fields = inspectionRuleDetailFields
		q.Set("detail", "true")
	}
	rules, err := ironicList(ctx, client, "inspection_rules", "inspection_rules", q, f.limit)
	if err != nil {
		return inspectionRuleErr(ctx, client, "listing inspection rules", err)
	}
	return o.WriteList(w, listTable(rules, fields))
}

func newInspectionRuleShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <rule>",
		Short: "Show details of an ironic inspection rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runInspectionRuleShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
	addFieldsAliases(cmd, o)
	return cmd
}

func runInspectionRuleShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	var rule map[string]any
	if err := ironicGet(ctx, client, joinPath("inspection_rules", id), &rule); err != nil {
		return inspectionRuleErr(ctx, client, "getting inspection rule "+id, err)
	}
	fields, values := showFields(rule, inspectionRuleDetailFields)
	return o.WriteSingle(w, fields, values)
}

// inspectionRuleAttrFlags are the attributes create and set share.
type inspectionRuleAttrFlags struct {
	description string
	priority    int
	phase       string
	conditions  string
	actions     string

	descriptionSet, prioritySet bool
}

func (f *inspectionRuleAttrFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.description, "description", "", "a brief explanation of the rule")
	fl.IntVar(&f.priority, "priority", 0, "the rule's precedence during execution")
	fl.StringVar(&f.phase, "phase", "", "the inspection phase the rule runs in, e.g. main")
	fl.StringVar(&f.conditions, "conditions", "",
		"conditions that trigger the rule: a JSON string, a JSON/YAML file, or '-' for standard input")
	fl.StringVar(&f.actions, "actions", "",
		"actions to run when the conditions match: a JSON string, a JSON/YAML file, or '-' for standard input")
}

func (f *inspectionRuleAttrFlags) changed(cmd *cobra.Command) {
	f.descriptionSet = cmd.Flags().Changed("description")
	f.prioritySet = cmd.Flags().Changed("priority")
}

func newInspectionRuleCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &inspectionRuleAttrFlags{}
	var uuid, sensitive string
	cmd := &cobra.Command{
		Use:   "create --actions <actions>",
		Short: "Create an ironic inspection rule (requires ironic API 1.96, OpenStack 2025.1)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.changed(cmd)
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runInspectionRuleCreate(ctx, client, o, f, uuid, sensitive, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&uuid, "uuid", "", "UUID of the rule")
	cmd.Flags().StringVar(&sensitive, "sensitive", "",
		"true to hide the rule's conditions and actions from reads; it cannot be unset later")
	_ = cmd.MarkFlagRequired("actions")
	return cmd
}

func runInspectionRuleCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	f *inspectionRuleAttrFlags, uuid, sensitive string, stdin io.Reader, w io.Writer,
) error {
	if err := o.CheckColumns(inspectionRuleColumns...); err != nil {
		return err
	}
	body := map[string]any{}
	setIf(body, "uuid", uuid)
	setIf(body, "phase", f.phase)
	if f.descriptionSet {
		body["description"] = f.description
	}
	if f.prioritySet {
		body["priority"] = f.priority
	}
	if sensitive != "" {
		b, err := strconv.ParseBool(sensitive)
		if err != nil {
			return fmt.Errorf("--sensitive %q: want true or false", sensitive)
		}
		body["sensitive"] = b
	}
	if err := f.readDocuments(body, stdin); err != nil {
		return err
	}
	var rule map[string]any
	if err := ironicPost(ctx, client, "inspection_rules", body, &rule); err != nil {
		return inspectionRuleErr(ctx, client, "creating inspection rule", err)
	}
	fields, values := showFields(rule, inspectionRuleDetailFields)
	return o.WriteSingle(w, fields, values)
}

// readDocuments parses --actions and --conditions into body.
func (f *inspectionRuleAttrFlags) readDocuments(body map[string]any, stdin io.Reader) error {
	if f.actions == "-" && f.conditions == "-" {
		return fmt.Errorf("only one of --actions and --conditions can read standard input")
	}
	for _, doc := range []struct{ key, arg string }{{"actions", f.actions}, {"conditions", f.conditions}} {
		if doc.arg == "" {
			continue
		}
		v, err := readJSONArg(doc.arg, "rule "+doc.key, stdin)
		if err != nil {
			return err
		}
		body[doc.key] = v
	}
	return nil
}

func newInspectionRuleSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &inspectionRuleAttrFlags{}
	cmd := &cobra.Command{
		Use:   "set <rule>",
		Short: "Set ironic inspection rule properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.changed(cmd)
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runInspectionRuleSet(ctx, client, o, args[0], f, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	f.register(cmd)
	return cmd
}

func runInspectionRuleSet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, f *inspectionRuleAttrFlags, stdin io.Reader, w io.Writer,
) error {
	if err := o.CheckColumns(inspectionRuleColumns...); err != nil {
		return err
	}
	docs := map[string]any{}
	if err := f.readDocuments(docs, stdin); err != nil {
		return err
	}
	// python-ironicclient patches every attribute with "add", which replaces
	// an existing member.
	var ops patchBuilder
	if f.descriptionSet {
		ops.add("/description", f.description)
	}
	if f.prioritySet {
		ops.add("/priority", f.priority)
	}
	if f.phase != "" {
		ops.add("/phase", f.phase)
	}
	for _, key := range []string{"actions", "conditions"} {
		if v, ok := docs[key]; ok {
			ops.add("/"+key, v)
		}
	}
	if len(ops) == 0 {
		return fmt.Errorf("inspection rule set requires at least one attribute flag")
	}
	return patchInspectionRule(ctx, client, o, id, ops, w)
}

func patchInspectionRule(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, ops patchBuilder, w io.Writer,
) error {
	var rule map[string]any
	if err := ironicPatch(ctx, client, joinPath("inspection_rules", id), ops, &rule); err != nil {
		return inspectionRuleErr(ctx, client, "updating inspection rule "+id, err)
	}
	fields, values := showFields(rule, inspectionRuleDetailFields)
	return o.WriteSingle(w, fields, values)
}

type inspectionRuleUnsetFlags struct {
	description, priority, phase bool
	actions, conditions          []int
}

func newInspectionRuleUnsetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &inspectionRuleUnsetFlags{}
	cmd := &cobra.Command{
		Use:   "unset <rule>",
		Short: "Unset ironic inspection rule properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runInspectionRuleUnset(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.description, "description", false, "unset the rule's description")
	fl.BoolVar(&f.priority, "priority", false, "unset the rule's priority, restoring the default")
	fl.BoolVar(&f.phase, "phase", false, "unset the rule's phase, restoring the default")
	fl.IntSliceVar(&f.actions, "action", nil, "index of an action to remove (repeatable)")
	fl.IntSliceVar(&f.conditions, "condition", nil, "index of a condition to remove (repeatable)")
	return cmd
}

func runInspectionRuleUnset(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	id string, f *inspectionRuleUnsetFlags, w io.Writer,
) error {
	if err := o.CheckColumns(inspectionRuleColumns...); err != nil {
		return err
	}
	var ops patchBuilder
	for path, set := range map[string]bool{"/description": f.description, "/priority": f.priority, "/phase": f.phase} {
		if set {
			ops.remove(path)
		}
	}
	slices.SortFunc(ops, func(a, b patchOp) int { return strings.Compare(a.Path, b.Path) })
	// Highest index first, so each removal leaves the indices still to come
	// pointing at the elements the operator named.
	for _, list := range []struct {
		key string
		idx []int
	}{{"actions", f.actions}, {"conditions", f.conditions}} {
		idx := slices.Clone(list.idx)
		slices.Sort(idx)
		idx = slices.Compact(idx)
		for i := len(idx) - 1; i >= 0; i-- {
			if idx[i] < 0 {
				return fmt.Errorf("--%s %d: an index is zero or more", strings.TrimSuffix(list.key, "s"), idx[i])
			}
			ops.remove("/" + list.key + "/" + strconv.Itoa(idx[i]))
		}
	}
	if len(ops) == 0 {
		return fmt.Errorf("inspection rule unset requires at least one attribute flag")
	}
	return patchInspectionRule(ctx, client, o, id, ops, w)
}

func newInspectionRuleDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <rule> [<rule> ...]",
		Short: "Delete ironic inspection rule(s)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runInspectionRuleDelete(ctx, client, args, cmd.OutOrStdout())
		},
	}
}

func runInspectionRuleDelete(ctx context.Context, client *gophercloud.ServiceClient, ids []string, w io.Writer) error {
	return batchdelete.Each(ids, func(id string) error {
		if err := ironicDelete(ctx, client, joinPath("inspection_rules", id)); err != nil {
			return inspectionRuleErr(ctx, client, "deleting inspection rule "+id, err)
		}
		_, err := fmt.Fprintf(w, "Deleted inspection rule %s\n", id)
		return err
	})
}
