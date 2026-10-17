package shell

import (
	"bytes"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// Completion is cobra's own: the hidden __complete command every cobra program
// carries, which walks the same tree, honours every ValidArgsFunction and
// RegisterFlagCompletionFunc, and is what the bash/zsh/fish scripts from `koc
// completion` call. The shell runs it in-process on a fresh tree instead of
// keeping a second implementation in step with the first.

// candidate is one completion cobra offered.
type candidate struct {
	value string
	desc  string
}

// completion is cobra's answer for one word.
type completion struct {
	candidates []candidate
	noSpace    bool
}

// complete asks cobra to complete toComplete after the given words.
func (s *Shell) complete(words []string, toComplete string) completion {
	root := s.cfg.NewRoot()
	words = s.cfg.Expand(root, words)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append(append([]string{cobra.ShellCompRequestCmd}, words...), toComplete))
	if err := root.Execute(); err != nil {
		return completion{}
	}
	return parseCompletion(out.String(), toComplete)
}

// parseCompletion reads __complete's output: one "value<TAB>description" line
// per candidate, then ":<directive>".
func parseCompletion(out, toComplete string) completion {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 {
		return completion{}
	}
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, ":") {
		return completion{}
	}
	d, err := strconv.Atoi(last[1:])
	if err != nil {
		return completion{}
	}
	dir := cobra.ShellCompDirective(d)
	if dir&cobra.ShellCompDirectiveError != 0 {
		return completion{}
	}
	var c completion
	c.noSpace = dir&cobra.ShellCompDirectiveNoSpace != 0
	for _, l := range lines[:len(lines)-1] {
		if l == "" {
			continue
		}
		value, desc, _ := strings.Cut(l, "\t")
		// A ValidArgsFunction may return everything and leave the filtering
		// to the shell, as cobra's own scripts do.
		if !strings.HasPrefix(value, toComplete) {
			continue
		}
		c.candidates = append(c.candidates, candidate{value: value, desc: desc})
	}
	return c
}

// autoComplete is the x/term AutoCompleteCallback: on Tab, it completes the
// word under the cursor, or lists the candidates when there is no single
// answer.
func (s *Shell) autoComplete(line string, pos int, key rune) (string, int, bool) {
	switch key {
	case keyInterrupt:
		// Ctrl-C at the prompt abandons the line, as in sh.
		return "", 0, true
	case '\t':
	default:
		return "", 0, false
	}

	head := line[:pos]
	words, inWord, err := splitPartial(head)
	if err != nil {
		return "", 0, false
	}
	words = stripPrefix(words)
	toComplete := ""
	if inWord && len(words) > 0 {
		toComplete = words[len(words)-1]
		words = words[:len(words)-1]
	}

	c := s.complete(words, toComplete)
	switch len(c.candidates) {
	case 0:
		return "", 0, false
	case 1:
		insert := c.candidates[0].value
		if !c.noSpace {
			insert = quoteArg(insert) + " "
		}
		return replaceWord(line, pos, inWord, insert)
	}

	if lcp := commonPrefix(c.candidates); len(lcp) > len(toComplete) {
		return replaceWord(line, pos, inWord, lcp)
	}
	s.listCandidates(c.candidates)
	return "", 0, false
}

// replaceWord puts insert in place of the word ending at pos (or at pos, when
// the cursor is after whitespace).
func replaceWord(line string, pos int, inWord bool, insert string) (string, int, bool) {
	start := pos
	if inWord {
		start = wordStart(line[:pos])
	}
	out := line[:start] + insert + line[pos:]
	return out, start + len(insert), true
}

// wordStart finds where the last word of head begins: after the last unquoted
// whitespace.
func wordStart(head string) int {
	start, quote := 0, rune(0)
	for i, r := range head {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t':
			start = i + 1
		}
	}
	return start
}

func commonPrefix(cs []candidate) string {
	p := cs[0].value
	for _, c := range cs[1:] {
		for !strings.HasPrefix(c.value, p) {
			p = p[:len(p)-1]
		}
	}
	return p
}

// listCandidates prints the candidates above the prompt, two columns when the
// descriptions fit.
func (s *Shell) listCandidates(cs []candidate) {
	cs = slices.Clone(cs)
	width := 0
	for _, c := range cs {
		width = max(width, len(c.value))
	}
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(c.value)
		if c.desc != "" {
			b.WriteString(strings.Repeat(" ", width-len(c.value)+2))
			b.WriteString(truncate(c.desc, max(s.width-width-4, 10)))
		}
		b.WriteString("\n")
	}
	s.writeAbovePrompt(b.String())
}
