package vaultcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/ftarasenko/go-openstackclient/internal/output"
	"github.com/ftarasenko/go-openstackclient/internal/vault"
)

// runKVList lists the immediate children of a KV v2 path. Folder entries keep
// Vault's trailing "/" so a subtree is distinguishable from a secret.
func runKVList(ctx context.Context, c *vault.Client, o *output.Options, path string, w io.Writer) error {
	keys, err := c.ListKV(ctx, c.KVMount(), path)
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			return fmt.Errorf("no keys under %q in mount %q", path, c.KVMount())
		}
		return err
	}
	sort.Strings(keys)

	rows := make([][]any, len(keys))
	for i, k := range keys {
		rows[i] = []any{k}
	}
	return o.WriteList(w, output.Table{Columns: []string{"Key"}, Rows: rows})
}

// runKVGet shows one secret as a Field/Value view. version 0 reads the latest.
func runKVGet(ctx context.Context, c *vault.Client, o *output.Options, path string, version int, w io.Writer) error {
	data, err := c.ReadKVDataAt(ctx, c.KVMount(), path, version)
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			return fmt.Errorf("no secret at %q in mount %q", path, c.KVMount())
		}
		return err
	}

	fields := make([]string, 0, len(data))
	for k := range data {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	values := make([]any, len(fields))
	for i, f := range fields {
		values[i] = data[f]
	}
	return o.WriteSingle(w, fields, values)
}
