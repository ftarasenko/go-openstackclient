package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// readVerbs are the leaf names that only read. Every other leaf may have
// changed something by the time it renders its result.
var readVerbs = map[string]bool{
	"list": true, "show": true, "get": true, "du": true, "info": true, "status": true,
	"showfile": true, "download": true, "export": true, "decrypt": true, "presign": true,
	"validate": true, "markers": true,
}

// guardRenderErrors makes a write verb that failed only while rendering its
// result say that the write went through. Without it `server create -c bogus`
// reports a bare "unknown column" after nova has built the server, and a
// script that retries on a non-zero exit builds a second one. Commands still
// call output.CheckColumns before their first write; this is the backstop for
// the ones whose columns are known only from the response.
func guardRenderErrors(root *cobra.Command) {
	for _, sub := range root.Commands() {
		guardRenderErrors(sub)
	}
	if root.RunE == nil || root.HasSubCommands() || readVerbs[root.Name()] {
		return
	}
	run := root.RunE
	root.RunE = func(cmd *cobra.Command, args []string) error {
		return explainRenderError(cmd.CommandPath(), run(cmd, args))
	}
}

func explainRenderError(path string, err error) error {
	var ce *output.ColumnError
	if !errors.As(err, &ce) || !ce.Rendering {
		return err
	}
	if ce.ID != "" {
		return fmt.Errorf("%s succeeded (id %s), but its output could not be rendered; do not re-run it: %w", path, ce.ID, err)
	}
	return fmt.Errorf("%s succeeded, but its output could not be rendered; do not re-run it: %w", path, err)
}
