package network

import (
	"context"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// This file is a raw-ServiceClient fallback per AGENTS.md: gophercloud v2.13.0
// vendors the individual networking/v2/extensions/* subpackages but has no
// package for the root /v2.0/extensions discovery endpoint itself, so there is
// no typed List/Get to call. The requests are two plain GETs isolated behind
// listNetworkExtensions / getNetworkExtension below; replace them with the
// typed calls if gophercloud ever adds the package. Neutron sends no
// microversion header, so nothing needs pinning.

// networkExtension is koc's DTO for one entry of the neutron extension
// discovery document.
type networkExtension struct {
	Alias       string `json:"alias"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Updated     string `json:"updated"`
	Links       []any  `json:"links"`
}

func listNetworkExtensions(ctx context.Context, client *gophercloud.ServiceClient) ([]networkExtension, error) {
	var body struct {
		Extensions []networkExtension `json:"extensions"`
	}
	resp, err := client.Get(ctx, client.ServiceURL("extensions"), &body, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return nil, err
	}
	return body.Extensions, nil
}

func getNetworkExtension(ctx context.Context, client *gophercloud.ServiceClient, alias string) (*networkExtension, error) {
	var body struct {
		Extension networkExtension `json:"extension"`
	}
	resp, err := client.Get(ctx, client.ServiceURL("extensions", alias), &body, nil)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if _, _, err = gophercloud.ParseResponse(resp, err); err != nil {
		return nil, err
	}
	return &body.Extension, nil
}

// newExtensionCommand builds "network extension ...".
//
// Command names follow upstream OSC (`openstack extension list --network`, which
// koc spells `network extension list` to keep it under the service it queries).
// UNVERIFIED against KeyStack docs (https://docs.keystack.ru/ returned HTTP 403
// at implementation time).
func newExtensionCommand(a *auth.Options, o *output.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "extension",
		Short: "Inspect the API extensions this neutron endpoint enables",
	}
	cmd.AddCommand(
		newExtensionListCommand(a, o),
		newExtensionShowCommand(a, o),
	)
	return cmd
}

func newExtensionListCommand(a *auth.Options, o *output.Options) *cobra.Command {
	var long bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List enabled network API extensions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			ctx := cmd.Context()
			client, err := newNetworkClient(ctx, a)
			if err != nil {
				return err
			}
			return runExtensionList(ctx, client, o, long, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&long, "long", false, "include each extension's description")
	return cmd
}

func runExtensionList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, long bool, w io.Writer) error {
	all, err := listNetworkExtensions(ctx, client)
	if err != nil {
		return fmt.Errorf("listing network extensions: %w", err)
	}
	cols := []string{"Name", "Alias"}
	if long {
		cols = append(cols, "Description", "Updated")
	}
	t := output.Table{Columns: cols, Rows: make([][]any, 0, len(all))}
	for _, e := range all {
		row := []any{e.Name, e.Alias}
		if long {
			row = append(row, e.Description, e.Updated)
		}
		t.Rows = append(t.Rows, row)
	}
	return o.WriteList(w, t)
}

func newExtensionShowCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <alias>",
		Short: "Show one network API extension by alias",
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
			return runExtensionShow(ctx, client, o, args[0], cmd.OutOrStdout())
		},
	}
}

func runExtensionShow(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, alias string, w io.Writer) error {
	e, err := getNetworkExtension(ctx, client, alias)
	if err != nil {
		return fmt.Errorf("showing network extension %q: %w", alias, err)
	}
	return o.WriteSingle(w,
		[]string{"name", "alias", "description", "updated"},
		[]any{e.Name, e.Alias, e.Description, e.Updated})
}
