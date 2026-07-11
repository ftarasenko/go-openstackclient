package identity

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/projects"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Flag names follow upstream OSC (`openstack project ...`). The KeyStack command
// reference at https://docs.keystack.ru/ returned HTTP 403 at implementation
// time, so these flags are UNVERIFIED against KeyStack and fall back to upstream
// OSC semantics.

func newProjectCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Manage projects"}
	cmd.AddCommand(
		newProjectListCommand(a, o),
		newProjectShowCommand(a, o),
		newProjectCreateCommand(a, o),
		newProjectDeleteCommand(a, o),
		newProjectSetCommand(a, o),
	)
	return cmd
}

func newProjectListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runProjectList(ctx, client, o, domain, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "filter by domain (name or ID)")
	return cmd
}

func runProjectList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, domainNameOrID string, w io.Writer) error {
	domainID, err := resolveDomainID(ctx, client, domainNameOrID)
	if err != nil {
		return err
	}
	pages, err := projects.List(client, projects.ListOpts{DomainID: domainID}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("listing projects: %w", err)
	}
	all, err := projects.ExtractProjects(pages)
	if err != nil {
		return fmt.Errorf("parsing project list: %w", err)
	}
	t := output.Table{Columns: []string{"ID", "Name", "Domain ID", "Enabled", "Description"}, Rows: make([][]any, 0, len(all))}
	for _, p := range all {
		t.Rows = append(t.Rows, []any{p.ID, p.Name, p.DomainID, p.Enabled, p.Description})
	}
	return o.WriteList(w, t)
}

func newProjectShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "show <project>",
		Short: "Show project details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runProjectShow(ctx, client, o, args[0], domain, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "domain owning the project (name or ID)")
	return cmd
}

func runProjectShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, nameOrID, domainNameOrID string, w io.Writer) error {
	domainID, err := resolveDomainID(ctx, client, domainNameOrID)
	if err != nil {
		return err
	}
	id, err := resolveProjectID(ctx, client, nameOrID, domainID)
	if err != nil {
		return err
	}
	p, err := projects.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("showing project %q: %w", nameOrID, err)
	}
	return o.WriteSingle(w,
		[]string{"ID", "Name", "Domain ID", "Enabled", "Description", "Parent ID"},
		[]any{p.ID, p.Name, p.DomainID, p.Enabled, p.Description, p.ParentID})
}

type projectWriteFlags struct {
	domain      string
	description string
	name        string
	properties  []string
	enable      bool
	enableSet   bool
	disableSet  bool
}

// parseProperties turns repeated key=value flags into a map for the API's
// free-form Extra fields.
func parseProperties(props []string) (map[string]any, error) {
	if len(props) == 0 {
		return nil, nil
	}
	m := make(map[string]any, len(props))
	for _, p := range props {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --property %q: expected key=value", p)
		}
		m[k] = v
	}
	return m, nil
}

func newProjectCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &projectWriteFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.enableSet = cmd.Flags().Changed("enable")
			f.disableSet = cmd.Flags().Changed("disable")
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runProjectCreate(ctx, client, o, args[0], f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.domain, "domain", "", "domain to create the project in (name or ID)")
	fl.StringVar(&f.description, "description", "", "project description")
	fl.StringArrayVar(&f.properties, "property", nil, "set a property key=value (repeatable)")
	fl.BoolVar(&f.enable, "enable", true, "enable the project (default)")
	fl.BoolVar(new(bool), "disable", false, "disable the project")
	return cmd
}

func runProjectCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, name string, f *projectWriteFlags, w io.Writer) error {
	domainID, err := resolveDomainID(ctx, client, f.domain)
	if err != nil {
		return err
	}
	extra, err := parseProperties(f.properties)
	if err != nil {
		return err
	}
	opts := projects.CreateOpts{
		Name:        name,
		DomainID:    domainID,
		Description: f.description,
		Enabled:     enabledFromFlags(f.enableSet, f.disableSet, f.enable),
		Extra:       extra,
	}
	p, err := projects.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating project %q: %w", name, err)
	}
	return o.WriteSingle(w,
		[]string{"ID", "Name", "Domain ID", "Enabled", "Description"},
		[]any{p.ID, p.Name, p.DomainID, p.Enabled, p.Description})
}

func newProjectDeleteCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "delete <project>",
		Short: "Delete a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runProjectDelete(ctx, client, args[0], domain)
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "domain owning the project (name or ID)")
	return cmd
}

func runProjectDelete(ctx context.Context, client *gophercloud.ServiceClient, nameOrID, domainNameOrID string) error {
	domainID, err := resolveDomainID(ctx, client, domainNameOrID)
	if err != nil {
		return err
	}
	id, err := resolveProjectID(ctx, client, nameOrID, domainID)
	if err != nil {
		return err
	}
	if err := projects.Delete(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting project %q: %w", nameOrID, err)
	}
	return nil
}

func newProjectSetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &projectWriteFlags{}
	cmd := &cobra.Command{
		Use:   "set <project>",
		Short: "Update a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			f.enableSet = cmd.Flags().Changed("enable")
			f.disableSet = cmd.Flags().Changed("disable")
			ctx := cmd.Context()
			client, err := newIdentityClient(ctx, a)
			if err != nil {
				return err
			}
			return runProjectSet(ctx, client, args[0], f, cmd.Flags().Changed("description"))
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.domain, "domain", "", "domain owning the project (name or ID)")
	fl.StringVar(&f.name, "name", "", "new project name")
	fl.StringVar(&f.description, "description", "", "new project description")
	fl.StringArrayVar(&f.properties, "property", nil, "set a property key=value (repeatable)")
	fl.BoolVar(&f.enable, "enable", false, "enable the project")
	fl.BoolVar(new(bool), "disable", false, "disable the project")
	return cmd
}

func runProjectSet(ctx context.Context, client *gophercloud.ServiceClient, nameOrID string, f *projectWriteFlags, descSet bool) error {
	domainID, err := resolveDomainID(ctx, client, f.domain)
	if err != nil {
		return err
	}
	id, err := resolveProjectID(ctx, client, nameOrID, domainID)
	if err != nil {
		return err
	}
	extra, err := parseProperties(f.properties)
	if err != nil {
		return err
	}
	opts := projects.UpdateOpts{
		Name:    f.name,
		Enabled: enabledFromFlags(f.enableSet, f.disableSet, f.enable),
		Extra:   extra,
	}
	if descSet {
		opts.Description = &f.description
	}
	if _, err := projects.Update(ctx, client, id, opts).Extract(); err != nil {
		return fmt.Errorf("updating project %q: %w", nameOrID, err)
	}
	return nil
}
