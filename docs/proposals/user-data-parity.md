# `--user-data` parity with upstream `openstack`

Scope: how a user-data file reaches nova on `server create`, measured against
`python-openstackclient` 8.2.0 and nova 26.3.0 (Zed, the floor from AGENTS.md
→ "Minimum supported cloud"). One finding is a silent data-corruption bug, not a
missing flag.

## Summary

| # | Divergence | Severity | Fix |
| --- | --- | --- | --- |
| 1 | koc sends the file **unencoded** when its bytes happen to parse as base64; nova then base64-*decodes* it and the guest boots with garbage user-data. No error anywhere. | **bug** | encode explicitly, stop relying on gophercloud's heuristic |
| 2 | An empty `--user-data` file is a hard error in koc; OSC silently sends no user-data | parity | warn on stderr, omit the field (OSC's wire behaviour, but not OSC's silence) |
| 3 | `server rebuild` has no `--user-data` / `--no-user-data` (nova 2.57) | missing feature | add both, gated at 2.57 |
| 4 | A missing/unreadable file is reported only after two API calls | polish | read the file during validation, before any network I/O |

1 and 3 are worth doing. 2 and 4 are small and ride along.

## What upstream does

`openstackclient/compute/v2/server.py` (OSC 8.2.0):

- `--user-data <path>`, help `"User data file to serve from the metadata
  server"` (`:1381-1385`). One string, no stdin convention — `-` is just a
  filename and `open()` fails on it.
- The file is read binary and **unconditionally** base64-encoded
  (`:1656-1666`):

  ```python
  with open(parsed_args.user_data, 'rb') as fh:
      # TODO(stephenfin): SDK should do this for us
      user_data = base64.b64encode(fh.read()).decode('utf-8')
  ```

  No content inspection, no size check, no charset assumption. An `OSError`
  becomes `Can't open '<path>': <exception>`.
- The result is attached only if truthy (`:2041-2042`): `if user_data:
  kwargs['user_data'] = user_data`. An **empty file** encodes to `''`, which is
  falsy, so OSC drops the key and creates the server with no user-data at all —
  silently. The SDK passes the string through verbatim (hence the TODO above),
  so what OSC computes is exactly what goes on the wire.

`server rebuild` has the same reader plus a mutually exclusive
`--no-user-data` (`:3498-3516`, `:3656-3683`), which sends `user_data: null` to
clear it.

> Upstream bug worth not copying: both rebuild paths gate on
> `supports_microversion(compute_client, '2.54')` while their own help text says
> 2.57. Nova added `user_data` to rebuild at **2.57**
> (`nova/api/openstack/compute/schemas/servers.py:430-440`, `rebuild_v257`);
> 2.54 is `key_name`. Between 2.54 and 2.56 OSC sends a field nova's schema
> rejects with `additionalProperties`. koc should gate on 2.57.

## What nova accepts

`nova/api/openstack/compute/schemas/servers.py:212-216` (create):

```python
'user_data': {'type': 'string', 'format': 'base64', 'maxLength': 65535}
```

- Not nullable on create (only the 2.0 variant, `create_v20`, allows `null`);
  nullable on rebuild from 2.57, which is what clears it.
- `maxLength` applies to the **encoded** string, so the largest file that can be
  sent is 49 149 bytes.
- The `base64` format checker (`nova/api/validation/validators.py:56-67`) calls
  `oslo_serialization.base64.decode_as_bytes`, which is plain
  `base64.b64decode(...)` with no `validate=True`
  (`oslo_serialization/base64.py:57-73`). Python's decoder **discards**
  characters outside the base64 alphabet, so nova's validation is lenient: it
  accepts far more than a strict decoder would, and never complains about the
  payload in divergence 1 below.

## What koc does today

`internal/cli/server/server.go:682` registers the flag, `:759-772` reads it,
`:805-807` assigns it to `servers.CreateOpts.UserData`. The struct's field is
`[]byte` and gophercloud decides the encoding for us
(`vendor/.../compute/v2/servers/requests.go:528-536`):

```go
if opts.UserData != nil {
    var userData string
    if _, err := base64.StdEncoding.DecodeString(string(opts.UserData)); err != nil {
        userData = base64.StdEncoding.EncodeToString(opts.UserData)
    } else {
        userData = string(opts.UserData)   // <- pass-through
    }
    b["user_data"] = &userData
}
```

### Divergence 1 — the heuristic corrupts ordinary files

"Already base64?" is decided by *trying to decode the file*. Go's base64 decoder
ignores `\r` and `\n`, so any file whose remaining bytes are all in
`[A-Za-z0-9+/]` with a length divisible by four takes the pass-through branch.
Observed against the vendored gophercloud:

| `--user-data` file | what koc sends | what OSC sends |
| --- | --- | --- |
| `#cloud-config\npackages: [fio]\n` | `I2Nsb3VkLWNvbmZpZwpwYWNrYWdlczogW2Zpb10K` | same ✅ |
| `runcmd\nls\n` | `runcmd\nls\n` ❌ | `cnVuY21kCmxzCg==` |
| `hostname\n` | `hostname\n` ❌ | `aG9zdG5hbWUK` |
| `deadbeef` | `deadbeef` ❌ | `ZGVhZGJlZWY=` |

Nova accepts the pass-through values (lenient decoder, above), stores them, and
the metadata service serves the *decoded* bytes: `runcmd\nls\n` reaches the guest
as the six bytes of `b64decode("runcmdls")`. Nothing fails. The operator gets an
ACTIVE instance that silently did not run its cloud-init, and `koc server show
--user-data` decodes the same garbage, so the client agrees with itself.

Files with `#`, `:`, `-`, `=` or a space are safe, which covers most real
cloud-configs and is why this has not bitten yet. Short scripts and generated
one-liners are not safe. The failure is silent, data-dependent and only
reproducible with the exact file, which is the worst combination to debug.

Note also that the heuristic's *intended* case is wrong for a drop-in
replacement: a file that is already base64 is passed through by koc and
double-encoded by OSC. The file's bytes are the user-data — that is the contract,
and koc should not second-guess it.

### Divergence 2 — empty file

`readUserData` (`:767-770`) rejects an empty file. OSC drops the key and
proceeds. An automation template that renders empty when there is nothing to
configure works under `openstack` and fails under `koc` — a real drop-in
regression, even though the resulting instance is identical either way.

### Divergence 3 — rebuild

`newServerRebuildCommand` (`internal/cli/server/actions.go:506-531`) registers
only `--image` and `--name`. gophercloud's `RebuildOpts` (`requests.go:759-785`)
has no `UserData` field at all — it still carries `Personality`, which nova
removed at 2.57 — so this needs a koc-owned builder, the same shape as
`serverCreateOptsExt`.

### Divergence 4 — ordering

`runServerCreate` validates, resolves the flavor (API call), parses properties,
builds scheduler hints (possibly another API call), and only then reads the
user-data file (`:805`). A typo'd path costs two round-trips before the error.

## Proposal

### P1 — encode explicitly (fixes 1, 2, 4)

`readUserData` returns the base64 text rather than the raw bytes:

```go
// readUserData loads the --user-data file and returns it base64-encoded, the
// way nova's schema wants it. Encoding here rather than leaving it to
// gophercloud's servers.CreateOpts is deliberate: that code base64-encodes the
// bytes only if they do not already decode as base64, and Go's decoder ignores
// newlines, so an ordinary file of alphanumerics whose length is a multiple of
// four (e.g. "runcmd\nls\n") takes the pass-through branch and reaches the
// guest as the decoded garbage instead. Handing gophercloud text that is
// already valid base64 pins the pass-through branch, so what is sent is exactly
// what upstream OSC sends (openstackclient/compute/v2/server.py, which
// unconditionally b64encodes the file).
```

- Empty file: warn on stderr — `warning: --user-data file %q is empty; creating
  the server without user data` — and leave `opts.UserData` nil so the key is
  omitted, matching OSC's request byte for byte. Precedent for the warning is
  the `--disk-overcommit` one at `actions.go:260`; OSC's silence here is not
  worth copying.
- Move the read into `validateServerCreate` (or immediately after it) so a bad
  path fails before the first API call.
- Add a bullet to AGENTS.md → "gophercloud v2 gotchas": `CreateOpts.UserData`
  guesses at the encoding, so pre-encode.

The fix depends on gophercloud keeping the pass-through branch. That is pinned
by a test asserting the exact `user_data` value on the wire, so a vendor bump
that changed it would fail loudly rather than start double-encoding. If that
ever happens, the alternative is to set `user_data` from `serverCreateOptsExt`
and leave `CreateOpts.UserData` unset, which removes the dependency entirely.

### P2 — `server rebuild --user-data` / `--no-user-data`

- Mutually exclusive (cobra's `MarkFlagsMutuallyExclusive`), matching OSC's
  argparse group.
- Gate both on `computeSupportsMicroversion(client, "2.57")` — nova's number,
  not OSC's 2.54 — and say `(nova 2.57 or later)` in the flag help, the way
  `--host` states 2.74. Zed's cap is 2.93, so this reaches the whole fleet.
- `--user-data` reuses P1's reader. `--no-user-data` sends JSON `null`.
- gophercloud's `RebuildOpts` cannot express the field, so add a
  `serverRebuildOptsExt` wrapping `servers.RebuildOptsBuilder` and splicing
  `user_data` into the body — same pattern and same comment style as
  `serverCreateOptsExt` (`create_blockdevice.go:219-231`).

Command-surface unchanged (no new leaf), so `docs/coverage.md` needs no edit;
this is flag-level parity, which that document does not count.

### P3 — optional, not recommended on its own

A client-side size check against nova's 49 149-byte raw ceiling would turn an
opaque 400 into a clear message. It is also a place to be wrong: a cloud with a
patched `maxLength` would be rejected by koc for a request it would accept.
If it lands at all it should decorate the error from a failed create rather than
pre-empt the request.

## Tests

Against the existing seam (`runServerCreate`, `runServerRebuild`) and the mock
endpoint, per AGENTS.md → "Testing":

1. **Regression for divergence 1** — table of the four files above, asserting
   the exact `user_data` string in the request body. `runcmd\nls\n` must arrive
   as `cnVuY21kCmxzCg==`. This is the test that pins gophercloud's behaviour.
2. Binary user-data (a gzip blob) round-trips to the same base64 Python's
   `b64encode` produces.
3. Empty file: no `user_data` key in the body, warning on stderr, exit 0.
4. Missing file: error mentions the path, and **no request reaches the mock**.
5. Rebuild: `--user-data` sends the encoded string; `--no-user-data` sends JSON
   `null`; both together are rejected by cobra; below 2.57 both fail with the
   microversion message and send nothing.

`create_blockdevice_test.go:165` already asserts a correct `user_data` value on
the happy path — extend that table rather than adding a parallel one.

## Not proposed

- `-` for stdin. OSC has no such convention and a file path is unambiguous.
- Decoding or validating the file's content (cloud-config lint, MIME
  multipart). The file's bytes are the user-data; nova and cloud-init own the
  rest.
- `server show --user-data` (koc-native, decodes what OSC prints raw) stays as
  it is.
