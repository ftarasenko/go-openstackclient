package keyvrm

import "github.com/spf13/cobra"

// kind enumerates the flag value types buildBody knows how to read.
type kind int

const (
	kindStr kind = iota
	kindInt
	kindBool
	kindFloat
	kindStrSlice
)

// flagSpec maps a CLI flag to the JSON body key it populates and its value type.
type flagSpec struct {
	flag string
	key  string
	kind kind
}

// buildBody assembles a PUT body from only the flags that were explicitly set,
// giving the KeyVRM API exclude_none update semantics.
func buildBody(cmd *cobra.Command, specs []flagSpec) map[string]any {
	fs := cmd.Flags()
	body := map[string]any{}
	for _, s := range specs {
		if !fs.Changed(s.flag) {
			continue
		}
		switch s.kind {
		case kindStr:
			v, _ := fs.GetString(s.flag)
			body[s.key] = v
		case kindInt:
			v, _ := fs.GetInt(s.flag)
			body[s.key] = v
		case kindBool:
			v, _ := fs.GetBool(s.flag)
			body[s.key] = v
		case kindFloat:
			v, _ := fs.GetFloat64(s.flag)
			body[s.key] = v
		case kindStrSlice:
			v, _ := fs.GetStringArray(s.flag)
			body[s.key] = v
		}
	}
	return body
}
