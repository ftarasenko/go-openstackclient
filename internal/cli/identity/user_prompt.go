package identity

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/cli/pwprompt"
)

// flagPasswordPrompt is upstream's `user create|set --password-prompt`: the
// password is read twice from the terminal without echo, so it never lands in
// the shell history or the process list the way --password does.
const flagPasswordPrompt = "password-prompt"

// promptUserPassword asks for a user's new password; tests replace it.
var promptUserPassword = readNewUserPassword

// addPasswordPromptFlag registers --password-prompt next to --password, which
// it replaces.
func addPasswordPromptFlag(cmd *cobra.Command, prompt *bool) {
	cmd.Flags().BoolVar(prompt, flagPasswordPrompt, false, "prompt interactively for the password")
	cmd.MarkFlagsMutuallyExclusive("password", flagPasswordPrompt)
}

// readNewUserPassword asks for the password twice, with upstream's wording.
func readNewUserPassword(w io.Writer) (string, error) {
	return pwprompt.Confirmed(w, pwprompt.Wording{
		Flag:     "--" + flagPasswordPrompt,
		First:    "User Password: ",
		Second:   "Repeat User Password: ",
		ReadErr:  "reading the password",
		Mismatch: "passwords do not match",
	})
}
