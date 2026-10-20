package baremetal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Vendor passthru: driver-specific methods a node's vendor interface exposes
// under /nodes/<node>/vendor_passthru, and a driver's default vendor interface
// under /drivers/<driver>/vendor_passthru. gophercloud has no call for either.

// passthruHTTPMethods are python-ironicclient's v1/utils.py HTTP_METHODS.
var passthruHTTPMethods = []string{http.MethodPost, http.MethodPut, http.MethodGet, http.MethodDelete, http.MethodPatch}

// passthruTarget is what a passthru verb addresses: a node or a driver.
type passthruTarget struct {
	collection string // "nodes" or "drivers"
	kind       string // "baremetal node" or "baremetal driver", for messages
}

var (
	nodePassthru   = passthruTarget{"nodes", "baremetal node"}
	driverPassthru = passthruTarget{"drivers", "baremetal driver"}
)

// newNodePassthruCommand builds "baremetal node passthru list|call".
func newNodePassthruCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newPassthruCommand(a, o, nodePassthru, "node", useListNode)
}

// newDriverPassthruCommand builds "baremetal driver passthru list|call": the
// driver-level methods of the driver's default vendor interface.
func newDriverPassthruCommand(a *auth.Options, o *output.Options) *cobra.Command {
	return newPassthruCommand(a, o, driverPassthru, "driver", "list <driver>")
}

func newPassthruCommand(a *auth.Options, o *output.Options, target passthruTarget, noun, listUse string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "passthru",
		Short: "Call a " + noun + "'s vendor passthru methods",
	}
	list := &cobra.Command{
		Use:   listUse,
		Short: "List a " + noun + "'s vendor passthru methods",
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
			return runPassthruList(ctx, client, o, target, args[0], cmd.OutOrStdout())
		},
	}
	var (
		callArgs   []string
		httpMethod string
	)
	call := &cobra.Command{
		Use:   "call <" + noun + "> <method>",
		Short: "Call a " + noun + "'s vendor passthru method",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := newBaremetalClient(ctx, a)
			if err != nil {
				return err
			}
			return runPassthruCall(ctx, client, target, args[0], args[1], strings.ToUpper(httpMethod), callArgs, cmd.OutOrStdout())
		},
	}
	call.Flags().StringArrayVar(&callArgs, "arg", nil, "argument key=value to pass to the method (repeatable)")
	call.Flags().StringVar(&httpMethod, "http-method", http.MethodPost,
		"HTTP method of the request: "+strings.Join(passthruHTTPMethods, ", "))
	cmd.AddCommand(list, call)
	return cmd
}

func runNodePassthruList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options, node string, w io.Writer) error {
	return runPassthruList(ctx, client, o, nodePassthru, node, w)
}

func runPassthruList(ctx context.Context, client *gophercloud.ServiceClient, o *output.Options,
	target passthruTarget, ref string, w io.Writer,
) error {
	var methods map[string]struct {
		HTTPMethods []string `json:"http_methods"`
		Async       bool     `json:"async"`
		Description string   `json:"description"`
		Attach      bool     `json:"attach"`
	}
	if err := ironicGet(ctx, client, joinPath(target.collection, ref, "vendor_passthru", "methods"), &methods); err != nil {
		return fmt.Errorf("listing vendor passthru methods of %s %s: %w", target.kind, ref, err)
	}
	fields := []string{"name", "http_methods", "async", "description", "attach"}
	t := output.Table{Columns: labels(fields), Rows: make([][]any, 0, len(methods))}
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for k := range methods {
			if !yield(k) {
				return
			}
		}
	}) {
		m := methods[name]
		t.Rows = append(t.Rows, []any{name, strings.Join(m.HTTPMethods, ", "), m.Async, m.Description, m.Attach})
	}
	return o.WriteList(w, t)
}

// runNodePassthruCall issues the method. Arguments travel as the JSON body,
// except for GET and DELETE, which upstream sends without one. The response
// belongs to the driver and has no fixed shape, so it is printed as JSON, as
// upstream prints it raw.
func runNodePassthruCall(ctx context.Context, client *gophercloud.ServiceClient, node, method, httpMethod string,
	args []string, w io.Writer,
) error {
	return runPassthruCall(ctx, client, nodePassthru, node, method, httpMethod, args, w)
}

func runPassthruCall(ctx context.Context, client *gophercloud.ServiceClient, target passthruTarget,
	ref, method, httpMethod string, args []string, w io.Writer,
) error {
	if !slices.Contains(passthruHTTPMethods, httpMethod) {
		return fmt.Errorf("--http-method %s: must be one of %s", httpMethod, strings.Join(passthruHTTPMethods, ", "))
	}
	body, err := parseKeyValMap(args)
	if err != nil {
		return fmt.Errorf("parsing --arg: %w", err)
	}
	if body == nil {
		body = map[string]any{}
	}
	opts := &gophercloud.RequestOpts{
		OkCodes:          []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent},
		KeepResponseBody: true,
	}
	if httpMethod != http.MethodGet && httpMethod != http.MethodDelete {
		opts.JSONBody = body
	}
	resp, err := client.Request(ctx, httpMethod, client.ServiceURL(joinPath(target.collection, ref, "vendor_passthru", method)), opts)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return fmt.Errorf("calling vendor passthru %s on %s %s: %w", method, target.kind, ref, err)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	// An attach method answers with a file, not JSON; print it as it came.
	var out any
	if json.Unmarshal(raw, &out) != nil {
		_, err := w.Write(raw)
		return err
	}
	if out == nil {
		return nil
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
