//go:build functional

package functional

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/cli"
)

// Every leaf command has a functional test. AGENTS.md states the rule; this
// enforces it, offline, on every commit (ci.yml runs this package with no
// cloud):
//
//   - A leaf is covered when some test in this package invokes it — a call to
//     a runner's run/ok/json/fails whose literal arguments resolve to that leaf
//     on the real command tree. It is read from the source, not from a run, so
//     a test gated to one cell (tap-mirror from 2025.1) still covers its leaf on
//     every cell, and a skipped test does not make the check flaky.
//   - uncovered.txt lists the leaves that predate the rule and have no test yet.
//     It only ever shrinks: a new command lands with its functional test, never
//     with a line here.
//
// The test fails on a leaf that is neither covered nor listed (a command added
// without a test), on a listed leaf that is now covered (delete the line), and
// on a listed path that is no longer a leaf (a renamed or removed command).
//
// UPDATE_UNCOVERED=1 rewrites uncovered.txt from the tree — for bootstrapping
// and for removing covered lines in bulk. Read the diff: an added line is a
// command shipped without a test.
func TestEveryLeafIsCovered(t *testing.T) {
	root := cli.NewRootCommand("functional")
	leaves := leafPaths(root)
	covered := coveredLeaves(t, root)

	const listFile = "uncovered.txt"
	if os.Getenv("UPDATE_UNCOVERED") == "1" {
		writeUncovered(t, listFile, leaves, covered)
		return
	}
	listed := readUncovered(t, listFile)

	for _, leaf := range leaves {
		switch {
		case covered[leaf] && listed[leaf]:
			t.Errorf("%s is covered now: delete it from %s", leaf, listFile)
		case !covered[leaf] && !listed[leaf]:
			t.Errorf("%s has no functional test: add one (AGENTS.md \"Testing\"); new commands are not added to %s", leaf, listFile)
		}
	}
	isLeaf := map[string]bool{}
	for _, l := range leaves {
		isLeaf[l] = true
	}
	for l := range listed {
		if !isLeaf[l] {
			t.Errorf("%s in %s is not a command any more: delete the line", l, listFile)
		}
	}
	t.Logf("%d of %d leaf commands have a functional test; %d still listed in %s",
		len(covered), len(leaves), len(listed), listFile)
}

// leafPaths is every runnable leaf of the tree, as "koc noun verb".
func leafPaths(root *cobra.Command) []string {
	var out []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			if sub.HasSubCommands() {
				walk(sub)
			} else {
				out = append(out, sub.CommandPath())
			}
		}
	}
	walk(root)
	sort.Strings(out)
	return out
}

// runnerMethods are the runner calls that execute koc, and the index of the
// first command-line argument in each.
var runnerMethods = map[string]int{"run": 1, "ok": 1, "fails": 1, "json": 2, "show": 1, "list": 1}

// coveredLeaves parses this package's tests and resolves every koc invocation
// in them to the leaf it runs.
func coveredLeaves(t *testing.T, root *cobra.Command) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			first, ok := runnerMethods[sel.Sel.Name]
			if !ok || len(call.Args) <= first {
				return true
			}
			if leaf := resolveLeaf(root, literalArgs(call.Args[first:])); leaf != nil {
				covered[leaf.CommandPath()] = true
			}
			return true
		})
	}
	return covered
}

// literalArgs is the leading run of string literals in a call's arguments,
// looking through append(x, "a", "b")... — the form tests use to add a command
// after flags. It stops at the first argument that is not a literal, which is
// always past the command words (a name, an ID, a path).
func literalArgs(args []ast.Expr) []string {
	var out []string
	for _, a := range args {
		switch v := a.(type) {
		case *ast.BasicLit:
			s, err := strconv.Unquote(v.Value)
			if err != nil || v.Kind != token.STRING {
				return out
			}
			out = append(out, s)
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "append" && len(v.Args) > 1 {
				return append(out, literalArgs(v.Args[1:])...)
			}
			return out
		default:
			return out
		}
	}
	return out
}

// resolveLeaf walks args down the command tree the way cobra does, skipping
// global flags (and the value of one that takes a value), and returns the leaf
// they name — nil for a group or an unknown word.
func resolveLeaf(root *cobra.Command, args []string) *cobra.Command {
	cmd := root
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			name := strings.TrimLeft(a, "-")
			if strings.Contains(name, "=") {
				continue
			}
			f := root.PersistentFlags().Lookup(name)
			if f == nil && len(name) == 1 {
				f = root.PersistentFlags().ShorthandLookup(name)
			}
			if f != nil && f.Value.Type() != "bool" && f.NoOptDefVal == "" {
				i++ // its value
			}
			continue
		}
		next := findSub(cmd, a)
		if next == nil {
			break
		}
		cmd = next
		if !cmd.HasSubCommands() {
			return cmd
		}
	}
	return nil
}

func findSub(c *cobra.Command, name string) *cobra.Command {
	for _, sub := range c.Commands() {
		if sub.Name() == name || sub.HasAlias(name) {
			return sub
		}
	}
	return nil
}

func readUncovered(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%v (UPDATE_UNCOVERED=1 creates it)", err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") {
			out[line] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func writeUncovered(t *testing.T, path string, leaves []string, covered map[string]bool) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`# Leaf commands with no functional test yet — debt from before the rule that
# every leaf has one (AGENTS.md "Testing"). TestEveryLeafIsCovered keeps this
# honest. It only ever shrinks: when a test lands for a leaf, delete its line;
# a new command lands with its test, never with a line here.
`)
	for _, l := range leaves {
		if !covered[l] {
			b.WriteString(l + "\n")
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}
