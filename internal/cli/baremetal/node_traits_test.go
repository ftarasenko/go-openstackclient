package baremetal

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

func TestNodeTraits(t *testing.T) {
	client, fake := newFakeIronic(t, map[string]fakeRoute{
		"GET /nodes/bm-1/traits":             {http.StatusOK, map[string]any{"traits": []string{"CUSTOM_A", "HW_CPU_X86_AVX2"}}},
		"PUT /nodes/bm-1/traits/CUSTOM_A":    {http.StatusNoContent, nil},
		"PUT /nodes/bm-1/traits/bad%20trait": {http.StatusBadRequest, map[string]any{"error_message": "invalid trait"}},
		"DELETE /nodes/bm-1/traits/CUSTOM_A": {http.StatusNoContent, nil},
		"DELETE /nodes/bm-1/traits":          {http.StatusNoContent, nil},
	})
	ctx := context.Background()

	var buf bytes.Buffer
	if err := runNodeTraitList(ctx, client, &output.Options{Format: output.FormatJSON}, "bm-1", &buf); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "trait list", decodeJSON[any](t, buf.String()), `[{"Traits":"CUSTOM_A"},{"Traits":"HW_CPU_X86_AVX2"}]`)

	// One bad trait fails alone; the others are still added, as upstream does.
	buf.Reset()
	err := runNodeTraitAdd(ctx, client, "bm-1", []string{"bad trait", "CUSTOM_A"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "failed to add trait bad trait") {
		t.Errorf("err = %v", err)
	}
	if buf.String() != "Added trait CUSTOM_A\n" {
		t.Errorf("output = %q", buf.String())
	}
	if fake.call("PUT /nodes/bm-1/traits/CUSTOM_A").Body != nil {
		t.Error("trait add sent a body")
	}

	buf.Reset()
	if err := runNodeTraitRemove(ctx, client, "bm-1", []string{"CUSTOM_A"}, false, &buf); err != nil || buf.String() != "Removed trait CUSTOM_A\n" {
		t.Errorf("remove: err = %v, output = %q", err, buf.String())
	}
	if err := runNodeTraitRemove(ctx, client, "bm-1", nil, true, &buf); err != nil {
		t.Error(err)
	}
	fake.call("DELETE /nodes/bm-1/traits")
}

// Both spellings upstream registers reach the same verbs.
func TestNodeTraitSpellings(t *testing.T) {
	root := newNodeCommand(nil, &output.Options{})
	for _, path := range [][]string{{"trait", "add"}, {"add", "trait"}, {"trait", "remove"}, {"remove", "trait"}, {"trait", "list"}} {
		cmd, rest, err := root.Find(path)
		if err != nil || len(rest) != 0 || cmd.RunE == nil {
			t.Errorf("node %s: not a runnable command (err %v)", strings.Join(path, " "), err)
		}
	}
	rm, _, _ := root.Find([]string{"trait", "remove"})
	rm.SetArgs([]string{"bm-1"})
	if err := rm.RunE(rm, []string{"bm-1"}); err == nil || !strings.Contains(err.Error(), "--all") {
		t.Errorf("trait remove with neither traits nor --all: err = %v", err)
	}
}
