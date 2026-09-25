package network

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/ikepolicies"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/vpnaas/ipsecpolicies"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Choice lists from upstream vpnaas/ikepolicy.py and ipsecpolicy.py (the two
// files carry identical algorithm and PFS lists).
var (
	vpnAuthAlgorithms = []string{"sha1", "sha256", "sha384", "sha512", "aes-xcbc", "aes-cmac"}

	vpnEncryptionAlgorithms = []string{
		"3des", "aes-128", "aes-192", "aes-256",
		"aes-128-ccm-8", "aes-192-ccm-8", "aes-256-ccm-8",
		"aes-128-ccm-12", "aes-192-ccm-12", "aes-256-ccm-12",
		"aes-128-ccm-16", "aes-192-ccm-16", "aes-256-ccm-16",
		"aes-128-gcm-8", "aes-192-gcm-8", "aes-256-gcm-8",
		"aes-128-gcm-12", "aes-192-gcm-12", "aes-256-gcm-12",
		"aes-128-gcm-16", "aes-192-gcm-16", "aes-256-gcm-16",
		"aes-128-ctr", "aes-192-ctr", "aes-256-ctr",
	}

	vpnPFSGroups = []string{
		"group2", "group5", "group14", "group15", "group16", "group17", "group18",
		"group19", "group20", "group21", "group22", "group23", "group24", "group25",
		"group26", "group27", "group28", "group29", "group30", "group31",
	}
)

// vpnPolicyFlags carries the flags the IKE and IPsec policy create/set verbs
// share; choices holds the noun-specific enumerated flags.
type vpnPolicyFlags struct {
	name          string
	description   string
	lifetime      []string
	choices       []*vpnChoice
	project       string
	projectDomain string
	// projectID is --project resolved by RunE, so the seam needs no identity client.
	projectID string
}

func ikePolicyChoices() []*vpnChoice {
	return []*vpnChoice{
		{flag: "auth-algorithm", attr: "auth_algorithm", help: "authentication algorithm", choices: vpnAuthAlgorithms},
		{flag: "encryption-algorithm", attr: "encryption_algorithm", help: "encryption algorithm", choices: vpnEncryptionAlgorithms},
		{flag: "phase1-negotiation-mode", attr: "phase1_negotiation_mode", help: "IKE phase1 negotiation mode", choices: []string{"main", "aggressive"}},
		{flag: "ike-version", attr: "ike_version", help: "IKE version for the policy", choices: []string{"v1", "v2"}},
		{flag: "pfs", attr: "pfs", help: "perfect forward secrecy", choices: vpnPFSGroups},
	}
}

func ipsecPolicyChoices() []*vpnChoice {
	return []*vpnChoice{
		{flag: "auth-algorithm", attr: "auth_algorithm", help: "authentication algorithm", choices: vpnAuthAlgorithms},
		{flag: "encapsulation-mode", attr: "encapsulation_mode", help: "encapsulation mode", choices: []string{"tunnel", "transport"}},
		{flag: "encryption-algorithm", attr: "encryption_algorithm", help: "encryption algorithm", choices: vpnEncryptionAlgorithms},
		{flag: "pfs", attr: "pfs", help: "perfect forward secrecy", choices: vpnPFSGroups},
		{flag: "transform-protocol", attr: "transform_protocol", help: "transform protocol", choices: []string{"esp", "ah", "ah-esp"}},
	}
}

// bindVPNPolicyCommon registers upstream's _get_common_parser flags; noun is
// "IKE" or "IPsec".
func bindVPNPolicyCommon(cmd *cobra.Command, f *vpnPolicyFlags, noun string) {
	fl := cmd.Flags()
	fl.StringVar(&f.description, flagDescription, "", "description of the "+noun+" policy")
	fl.StringArrayVar(&f.lifetime, "lifetime", nil,
		noun+" lifetime as units=<units>,value=<value> (units: seconds; value: integer >= 60)")
	bindVPNChoices(cmd, f.choices)
}

// bindVPNPolicyCreate adds the create-only flags.
func bindVPNPolicyCreate(cmd *cobra.Command, f *vpnPolicyFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.project, flagProject, "", "owner's project (name or ID)")
	fl.StringVar(&f.projectDomain, flagProjectDomain, "", projectDomainHelp)
}

// vpnPolicyAttrs is upstream's _get_common_attrs plus name and project.
func vpnPolicyAttrs(f *vpnPolicyFlags) (map[string]any, error) {
	attrs := map[string]any{}
	if f.description != "" {
		attrs["description"] = f.description
	}
	if err := applyVPNChoices(attrs, f.choices); err != nil {
		return nil, err
	}
	lifetime, err := parseVPNLifetime(f.lifetime)
	if err != nil {
		return nil, err
	}
	if lifetime != nil {
		attrs["lifetime"] = lifetime
	}
	if f.projectID != "" {
		attrs["project_id"] = f.projectID
	}
	if f.name != "" {
		attrs["name"] = f.name
	}
	return attrs, nil
}

// vpnLifetime renders a lifetime attribute; absent when neutron sent none.
func vpnLifetime(units string, value int) any {
	if units == "" && value == 0 {
		return nil
	}
	return map[string]any{"units": units, "value": value}
}

// newVPNPolicyCreateCommand builds the IKE/IPsec create verb.
func newVPNPolicyCreateCommand(a *auth.Options, o *output.Options, noun string, choices []*vpnChoice,
	run func(context.Context, *gophercloud.ServiceClient, *output.Options, *vpnPolicyFlags, io.Writer) error,
) *cobra.Command {
	f := &vpnPolicyFlags{choices: choices}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an " + noun + " policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, session, err := newNetworkSession(ctx, a)
			if err != nil {
				return err
			}
			if f.projectID, err = resolveProjectRef(ctx, session, f.project, f.projectDomain); err != nil {
				return err
			}
			f.name = args[0]
			return run(ctx, client, o, f, cmd.OutOrStdout())
		},
	}
	bindVPNPolicyCommon(cmd, f, noun)
	bindVPNPolicyCreate(cmd, f)
	return cmd
}

// newVPNPolicySetCommand builds the IKE/IPsec set verb.
func newVPNPolicySetCommand(a *auth.Options, o *output.Options, noun, use string, choices []*vpnChoice,
	run func(context.Context, *gophercloud.ServiceClient, *output.Options, string, *vpnPolicyFlags, io.Writer) error,
) *cobra.Command {
	f := &vpnPolicyFlags{choices: choices}
	cmd := &cobra.Command{
		Use:   use,
		Short: "Set " + noun + " policy properties",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return run(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	bindVPNPolicyCommon(cmd, f, noun)
	cmd.Flags().StringVar(&f.name, "name", "", "new name for the "+noun+" policy")
	return cmd
}

// ---- vpn ike policy -------------------------------------------------------

func newIKEPolicyCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "policy", Short: "Manage IKE policies"}
	cmd.AddCommand(
		newVPNPolicyCreateCommand(a, o, "IKE", ikePolicyChoices(), runIKEPolicyCreate),
		newVPNDeleteCommand(a, o, "delete <ike-policy> [<ike-policy> ...]", "Delete IKE policy (policies)", runIKEPolicyDelete),
		newVPNListCommand(a, o, "List IKE policies", runIKEPolicyList),
		newVPNPolicySetCommand(a, o, "IKE", "set <ike-policy>", ikePolicyChoices(), runIKEPolicySet),
		newVPNShowCommand(a, o, "show <ike-policy>", "Show IKE policy details", runIKEPolicyShow),
	)
	return cmd
}

func resolveIKEPolicyID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "IKE policy", nameOrID, func(c *gophercloud.ServiceClient) ([]ikepolicies.Policy, error) {
		pages, err := ikepolicies.List(c, ikepolicies.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return ikepolicies.ExtractPolicies(pages)
	}, func(p ikepolicies.Policy) string { return p.ID })
}

func ikePolicyShowFields(p *ikepolicies.Policy) ([]string, []any) {
	return []string{
		"Authentication Algorithm", "Description", "Encryption Algorithm", "ID",
		"IKE Version", "Lifetime", "Name", "Perfect Forward Secrecy (PFS)",
		"Phase1 Negotiation Mode", "Project",
	}, []any{
		p.AuthAlgorithm, p.Description, p.EncryptionAlgorithm, p.ID,
		p.IKEVersion, vpnLifetime(p.Lifetime.Units, p.Lifetime.Value), p.Name, p.PFS,
		p.Phase1NegotiationMode, p.ProjectID,
	}
}

func runIKEPolicyList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, long bool, w io.Writer) error {
	pages, err := ikepolicies.List(client, nil).AllPages(ctx)
	if err != nil {
		return explainVPNaaS(ctx, client, fmt.Errorf("listing IKE policies: %w", err))
	}
	all, err := ikepolicies.ExtractPolicies(pages)
	if err != nil {
		return fmt.Errorf("parsing IKE policy list: %w", err)
	}
	cols := []string{"ID", "Name", "Authentication Algorithm", "Encryption Algorithm", "IKE Version", "Perfect Forward Secrecy (PFS)"}
	if long {
		cols = append(cols, "Description", "Phase1 Negotiation Mode", "Project", "Lifetime")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, p := range all {
		row := []any{p.ID, p.Name, p.AuthAlgorithm, p.EncryptionAlgorithm, p.IKEVersion, p.PFS}
		if long {
			row = append(row, p.Description, p.Phase1NegotiationMode, p.ProjectID, vpnLifetime(p.Lifetime.Units, p.Lifetime.Value))
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func runIKEPolicyShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) (err error) {
	defer func() { err = explainVPNaaS(ctx, client, err) }()
	id, err := resolveIKEPolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := vpnExtracted(ikepolicies.Get(ctx, client, id).Extract())
	if err != nil {
		return fmt.Errorf("getting IKE policy %s: %w", ref, err)
	}
	fields, values := ikePolicyShowFields(p)
	return o.WriteSingle(w, fields, values)
}

func runIKEPolicyDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return explainVPNaaS(ctx, client, runVPNDelete(ctx, client, "IKE policy", refs, resolveIKEPolicyID,
		func(id string) error { return ikepolicies.Delete(ctx, client, id).ExtractErr() }, w))
}

func runIKEPolicyCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *vpnPolicyFlags, w io.Writer) (err error) {
	defer func() { err = explainVPNaaS(ctx, client, err) }()
	attrs, err := vpnPolicyAttrs(f)
	if err != nil {
		return err
	}
	p, err := vpnExtracted(ikepolicies.Create(ctx, client, vpnBody{key: "ikepolicy", attrs: attrs}).Extract())
	if err != nil {
		return fmt.Errorf("creating IKE policy: %w", err)
	}
	fields, values := ikePolicyShowFields(p)
	return o.WriteSingle(w, fields, values)
}

func runIKEPolicySet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *vpnPolicyFlags, w io.Writer) (err error) {
	defer func() { err = explainVPNaaS(ctx, client, err) }()
	attrs, err := vpnPolicyAttrs(f)
	if err != nil {
		return err
	}
	if len(attrs) == 0 {
		return fmt.Errorf("vpn ike policy set requires at least one attribute flag")
	}
	id, err := resolveIKEPolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := vpnExtracted(ikepolicies.Update(ctx, client, id, vpnBody{key: "ikepolicy", attrs: attrs}).Extract())
	if err != nil {
		return fmt.Errorf("updating IKE policy %s: %w", ref, err)
	}
	fields, values := ikePolicyShowFields(p)
	return o.WriteSingle(w, fields, values)
}

// ---- vpn ipsec policy -----------------------------------------------------

func newIPsecPolicyCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "policy", Short: "Manage IPsec policies"}
	cmd.AddCommand(
		newVPNPolicyCreateCommand(a, o, "IPsec", ipsecPolicyChoices(), runIPsecPolicyCreate),
		newVPNDeleteCommand(a, o, "delete <ipsec-policy> [<ipsec-policy> ...]", "Delete IPsec policy (policies)", runIPsecPolicyDelete),
		newVPNListCommand(a, o, "List IPsec policies", runIPsecPolicyList),
		newVPNPolicySetCommand(a, o, "IPsec", "set <ipsec-policy>", ipsecPolicyChoices(), runIPsecPolicySet),
		newVPNShowCommand(a, o, "show <ipsec-policy>", "Show IPsec policy details", runIPsecPolicyShow),
	)
	return cmd
}

func resolveIPsecPolicyID(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string) (string, error) {
	return resolveByName(client, "IPsec policy", nameOrID, func(c *gophercloud.ServiceClient) ([]ipsecpolicies.Policy, error) {
		pages, err := ipsecpolicies.List(c, ipsecpolicies.ListOpts{Name: nameOrID}).AllPages(ctx)
		if err != nil {
			return nil, err
		}
		return ipsecpolicies.ExtractPolicies(pages)
	}, func(p ipsecpolicies.Policy) string { return p.ID })
}

func ipsecPolicyShowFields(p *ipsecpolicies.Policy) ([]string, []any) {
	return []string{
		"Authentication Algorithm", "Description", "Encapsulation Mode", "Encryption Algorithm",
		"ID", "Lifetime", "Name", "Perfect Forward Secrecy (PFS)", "Project", "Transform Protocol",
	}, []any{
		p.AuthAlgorithm, p.Description, p.EncapsulationMode, p.EncryptionAlgorithm,
		p.ID, vpnLifetime(p.Lifetime.Units, p.Lifetime.Value), p.Name, p.PFS, p.ProjectID, p.TransformProtocol,
	}
}

func runIPsecPolicyList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, long bool, w io.Writer) error {
	pages, err := ipsecpolicies.List(client, nil).AllPages(ctx)
	if err != nil {
		return explainVPNaaS(ctx, client, fmt.Errorf("listing IPsec policies: %w", err))
	}
	all, err := ipsecpolicies.ExtractPolicies(pages)
	if err != nil {
		return fmt.Errorf("parsing IPsec policy list: %w", err)
	}
	cols := []string{"ID", "Name", "Authentication Algorithm", "Encapsulation Mode", "Transform Protocol", "Encryption Algorithm"}
	if long {
		cols = append(cols, "Perfect Forward Secrecy (PFS)", "Description", "Project", "Lifetime")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, p := range all {
		row := []any{p.ID, p.Name, p.AuthAlgorithm, p.EncapsulationMode, p.TransformProtocol, p.EncryptionAlgorithm}
		if long {
			row = append(row, p.PFS, p.Description, p.ProjectID, vpnLifetime(p.Lifetime.Units, p.Lifetime.Value))
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func runIPsecPolicyShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) (err error) {
	defer func() { err = explainVPNaaS(ctx, client, err) }()
	id, err := resolveIPsecPolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := vpnExtracted(ipsecpolicies.Get(ctx, client, id).Extract())
	if err != nil {
		return fmt.Errorf("getting IPsec policy %s: %w", ref, err)
	}
	fields, values := ipsecPolicyShowFields(p)
	return o.WriteSingle(w, fields, values)
}

func runIPsecPolicyDelete(ctx context.Context, client *gophercloud.ServiceClient, refs []string, w io.Writer) error {
	return explainVPNaaS(ctx, client, runVPNDelete(ctx, client, "IPsec policy", refs, resolveIPsecPolicyID,
		func(id string) error { return ipsecpolicies.Delete(ctx, client, id).ExtractErr() }, w))
}

func runIPsecPolicyCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, f *vpnPolicyFlags, w io.Writer) (err error) {
	defer func() { err = explainVPNaaS(ctx, client, err) }()
	attrs, err := vpnPolicyAttrs(f)
	if err != nil {
		return err
	}
	p, err := vpnExtracted(ipsecpolicies.Create(ctx, client, vpnBody{key: "ipsecpolicy", attrs: attrs}).Extract())
	if err != nil {
		return fmt.Errorf("creating IPsec policy: %w", err)
	}
	fields, values := ipsecPolicyShowFields(p)
	return o.WriteSingle(w, fields, values)
}

func runIPsecPolicySet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, f *vpnPolicyFlags, w io.Writer) (err error) {
	defer func() { err = explainVPNaaS(ctx, client, err) }()
	attrs, err := vpnPolicyAttrs(f)
	if err != nil {
		return err
	}
	if len(attrs) == 0 {
		return fmt.Errorf("vpn ipsec policy set requires at least one attribute flag")
	}
	id, err := resolveIPsecPolicyID(ctx, client, ref)
	if err != nil {
		return err
	}
	p, err := vpnExtracted(ipsecpolicies.Update(ctx, client, id, vpnBody{key: "ipsecpolicy", attrs: attrs}).Extract())
	if err != nil {
		return fmt.Errorf("updating IPsec policy %s: %w", ref, err)
	}
	fields, values := ipsecPolicyShowFields(p)
	return o.WriteSingle(w, fields, values)
}
