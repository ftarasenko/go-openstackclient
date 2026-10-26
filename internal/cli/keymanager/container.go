package keymanager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/keymanager/v1/containers"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func newContainerCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "container",
		Short: "Manage secret containers",
	}
	cmd.AddCommand(
		newContainerListCommand(a, o),
		newContainerGetCommand(a, o),
		newContainerCreateCommand(a, o),
		newContainerDeleteCommand(a),
	)
	return cmd
}

// containerColumns are upstream's generic ContainerFormatter columns, which
// `secret container list` uses for every type.
var containerColumns = []string{"Container href", "Name", "Created", "Status", "Type", "Secrets", "Consumers"}

// containerSecretNames are the secret names an rsa or certificate container
// holds, in upstream's column order; a generic container takes any name.
var containerSecretNames = map[string][]string{
	"rsa":         {"public_key", "private_key", "private_key_passphrase"},
	"certificate": {"certificate", "intermediates", "private_key", "private_key_passphrase"},
}

// containerSecretColumns are the per-secret column headers of the rsa and
// certificate formatters.
var containerSecretColumns = map[string]string{
	"public_key": "Public Key", "private_key": "Private Key", "private_key_passphrase": "PK Passphrase",
	"certificate": "Certificate", "intermediates": "Intermediates",
}

func joinLines[T any](items []T, line func(T) string) any {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = line(it)
	}
	return strings.Join(out, "\n")
}

func containerConsumers(c *containers.Container) any {
	return joinLines(c.Consumers, func(r containers.ConsumerRef) string { return r.Name + "=" + r.URL })
}

func containerListRow(c *containers.Container) []any {
	secretsCell := joinLines(c.SecretRefs, func(r containers.SecretRef) string {
		if r.Name == "" {
			return r.SecretRef
		}
		return r.Name + "=" + r.SecretRef
	})
	return []any{c.ContainerRef, c.Name, c.Created, c.Status, c.Type, secretsCell, containerConsumers(c)}
}

// containerShow is the field list `secret container get` prints: the generic
// layout, or for an rsa/certificate container one column per named secret.
func containerShow(c *containers.Container) ([]string, []any) {
	names, typed := containerSecretNames[c.Type]
	if !typed {
		return containerColumns, containerListRow(c)
	}
	fields := []string{"Container href", "Name", "Created", "Status", "Type"}
	values := []any{c.ContainerRef, c.Name, c.Created, c.Status, c.Type}
	for _, n := range names {
		var ref any
		for _, s := range c.SecretRefs {
			if s.Name == n {
				ref = s.SecretRef
			}
		}
		fields = append(fields, containerSecretColumns[n])
		values = append(values, ref)
	}
	return append(fields, "Consumers"), append(values, containerConsumers(c))
}

// containerListOpts adds barbican's type filter, which gophercloud's
// containers.ListOpts lacks.
type containerListOpts struct {
	Limit  int    `q:"limit"`
	Offset int    `q:"offset"`
	Name   string `q:"name"`
	Type   string `q:"type"`
}

// ToContainerListQuery implements containers.ListOptsBuilder.
func (opts containerListOpts) ToContainerListQuery() (string, error) {
	q, err := gophercloud.BuildQueryString(opts)
	if err != nil {
		return "", err
	}
	return q.String(), nil
}

func newContainerListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &containerListOpts{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List secret containers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runContainerList(ctx, client, o, *f, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.IntVarP(&f.Limit, "limit", "l", 10, "the number of containers to list (maximum: 100)")
	fl.IntVarP(&f.Offset, "offset", "o", 0, "the page offset")
	fl.StringVarP(&f.Name, "name", "n", "", "filter by container name")
	fl.StringVarP(&f.Type, "type", "t", "", "filter by container type")
	return cmd
}

func runContainerList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, opts containerListOpts, w io.Writer) error {
	page, err := firstPage(ctx, containers.List(client, opts))
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}
	t := output.Table{Columns: containerColumns}
	if page == nil {
		return o.WriteList(w, t)
	}
	all, err := containers.ExtractContainers(page)
	if err != nil {
		return fmt.Errorf("parsing container list: %w", err)
	}
	for i := range all {
		t.Rows = append(t.Rows, containerListRow(&all[i]))
	}
	return o.WriteList(w, t)
}

func newContainerGetCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "get <uri>",
		Short: "Retrieve a secret container by its URI",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runContainerGet(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runContainerGet(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, ref string, w io.Writer) error {
	id, err := refUUID(ref, "Container")
	if err != nil {
		return err
	}
	return showContainer(ctx, client, o, id, w)
}

func showContainer(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, id string, w io.Writer) error {
	c, err := containers.Get(ctx, client, id).Extract()
	if err != nil {
		return fmt.Errorf("getting container %s: %w", id, err)
	}
	fields, values := containerShow(c)
	return o.WriteSingle(w, fields, values)
}

// containerCreateFlags holds `secret container create`'s options, named as
// python-barbicanclient's CreateContainer names them; UNVERIFIED against
// KeyStack.
type containerCreateFlags struct {
	name, typ string
	secrets   []string
}

func newContainerCreateCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &containerCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Store a secret container in barbican",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			opts, err := f.createOpts()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runContainerCreate(ctx, client, o, opts, cmd.OutOrStdout())
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.name, "name", "n", "", "a human-friendly name")
	fl.StringVar(&f.typ, "type", "generic", "the container type: generic, rsa or certificate")
	fl.StringArrayVarP(&f.secrets, "secret", "s", nil,
		`a secret to store in the container, as name=<secret href>; repeatable (e.g. --secret "private_key=https://cloud.example.com/v1/secrets/<id>")`)
	return cmd
}

// createOpts mirrors CreateContainer: at least one --secret is required, an
// entry without exactly one "=" is skipped, and an rsa or certificate
// container keeps only the names its type defines.
func (f *containerCreateFlags) createOpts() (containers.CreateOpts, error) {
	opts := containers.CreateOpts{Name: f.name, Type: containers.ContainerType(f.typ)}
	allowed, typed := containerSecretNames[f.typ]
	if !typed && f.typ != "generic" {
		return opts, fmt.Errorf("invalid container type %q: want generic, rsa or certificate", f.typ)
	}
	if len(f.secrets) == 0 {
		return opts, errors.New("must supply at least one --secret")
	}
	for _, s := range f.secrets {
		if strings.Count(s, "=") != 1 {
			continue
		}
		name, ref, _ := strings.Cut(s, "=")
		if typed && !slices.Contains(allowed, name) {
			continue
		}
		opts.SecretRefs = append(opts.SecretRefs, containers.SecretRef{Name: name, SecretRef: ref})
	}
	return opts, nil
}

func runContainerCreate(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, opts containers.CreateOpts, w io.Writer) error {
	created, err := containers.Create(ctx, client, opts).Extract()
	if err != nil {
		return fmt.Errorf("creating container: %w", err)
	}
	id, err := refUUID(created.ContainerRef, "Container")
	if err != nil {
		return fmt.Errorf("creating container: barbican answered container_ref %q: %w", created.ContainerRef, err)
	}
	return showContainer(ctx, client, o, id, w)
}

func newContainerDeleteCommand(a *auth.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <uri>",
		Short: "Delete a secret container by its URI",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newKeyManagerClient(ctx, a)
			if err != nil {
				return err
			}
			return runContainerDelete(ctx, client, args[0])
		},
	}
}

func runContainerDelete(ctx context.Context, client *gophercloud.ServiceClient, ref string) error {
	id, err := refUUID(ref, "Container")
	if err != nil {
		return err
	}
	if err := containers.Delete(ctx, client, id).ExtractErr(); err != nil {
		return fmt.Errorf("deleting container %s: %w", id, err)
	}
	return nil
}
