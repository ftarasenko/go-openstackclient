# `koc shell` and the embedded profile

`koc shell` is an interactive REPL that runs koc commands and nothing else. It
works in any terminal, and it is what an embedding host — a web console that
offers its signed-in users a terminal — attaches to: the host starts one
short-lived sandbox per browser session holding only koc, connects the browser
to `koc shell` through a PTY, hands over the user's Keystone token, and records
what the user ran. This page covers the shell, the variables a host sets, the
scope file, the embedded profile and the audit log.

## Using the shell

```text
$ koc shell
koc [RegionOne · demo] $ server list --long
…
koc [RegionOne · demo] $ server lst
koc: unknown command "lst" for "koc server"
koc [RegionOne · demo] 2 $ network li<Tab>
koc [RegionOne · demo] 2 $ network list
```

- **A line is a koc command** without the leading `koc`. A pasted `koc ` or
  `openstack ` prefix is dropped, and command and flag abbreviations expand as
  they do on the command line (`server li` is `server list`).
- **Arguments are split like a POSIX shell's**: single quotes, double quotes and
  backslash escapes, and `#` starts a comment. There are **no** pipes,
  redirects, globbing, variables, `;`, `&&`, `&` or subshells. A line using one
  unquoted is refused with the feature named (`pipes (|) are not supported;
  quote the character to pass it literally`) rather than handed to koc.
- **Every line runs on a fresh command tree**, so a flag never leaks into the
  next line, and it authenticates afresh (one Keystone request per line).
- **A failing line never ends the shell.** Its exit status — the one the same
  command would exit with on its own: 1 for a failure, 2 for a usage error, 130
  for an interrupt — shows in the prompt until the next line.
- **Ctrl-C** cancels the running command (its context, exactly as SIGINT does
  outside the shell) and returns to the prompt; at the prompt it abandons the
  half-typed line. **Ctrl-D** on an empty line or `exit` leaves with status 0.
  SIGTERM ends the shell once the current line is done.
- **Tab** completes commands, flags and, where koc can, values — it is cobra's
  own completion engine, the one `koc completion` scripts use. A second Tab
  lists the candidates. **Up/Down** walk the history.
- **Full-screen commands work**: the shell runs on a real terminal, re-reads its
  size before every line, and leaves raw mode while a command runs, so
  `--watch` and the gauges behave as they do outside it.
- **Global flags given to `koc shell` itself apply to every line**, as though
  typed on it: `koc shell -f json` makes every line print JSON.

The prompt is `koc [<region> · <project>] $ `, with the project's name, or the
first eight characters of its ID when no name is known. It never takes more
than half the terminal's width (at least 24 columns), so the names are
truncated with `…` before the line runs out of room on an 80-column terminal.

Built-ins:

| Built-in | Does |
| --- | --- |
| `help [command]` | koc's own help (`help server list`) |
| `scope` | the auth host, region, project, user and token expiry — never the token |
| `clear` | clears the screen |
| `exit` | leaves the shell (status 0) |

When stdin is not a terminal the shell reads plain lines without prompts, so
`printf 'server list\nvolume list\n' | koc shell` runs as a script. It still
exits 0; check each line's output or the audit log.

## Environment

| Variable | Meaning |
| --- | --- |
| `KOC_HOME` | the only directory koc reads configuration from or writes state to (below) |
| `KOC_PROFILE` | `embedded` selects the embedded profile; a `koc-embedded` binary is always in it |
| `KOC_SCOPE_FILE` | the scope file (below); relative paths resolve inside `KOC_HOME` |
| `KOC_HISTORY_FILE` | persist history here; without it history is in memory only |
| `KOC_AUDIT_FD` | write audit events to this inherited file descriptor (3 or above) |
| `KOC_AUDIT_FILE` | … or append them to this file; relative paths resolve inside `KOC_HOME` |

Without any of them koc behaves exactly as before.

### `KOC_HOME`

With `KOC_HOME` set, that directory replaces every implicit location koc reads
configuration from: the `clouds.yaml`/`secure.yaml` search path (working
directory, `$HOME`, `XDG_CONFIG_HOME`, `XDG_CONFIG_DIRS`, `/etc/openstack`),
`~/.vault-token` and `~/.kube/config` all resolve inside it. A file the
operator names explicitly — `OS_CLIENT_CONFIG_FILE`, `VAULT_TOKEN_FILE`,
`KUBECONFIG`, `--kubeconfig` — still wins in the default profile. In the
embedded profile `KOC_HOME` defaults to `$TMPDIR/koc-home`, explicit config
paths are ignored, and `$HOME`, `XDG_*` and `/etc` are never read.

### History

History is kept in memory (the last 1000 lines) unless `KOC_HISTORY_FILE` names
a file: then earlier sessions' lines are loaded from it and each new line is
appended (created `0600`). A secret flag's value is replaced by `***` before a
line reaches the file — the line is recalled as typed for the rest of the
session, but the file never holds the secret.

## The scope file

The host hands koc the user's token and scope in a JSON file, names it in
`KOC_SCOPE_FILE`, and rewrites it to rotate the token or switch region or
project. The shell re-reads it before every line (by modification time and
size, then content hash), so neither needs a restart, which would lose the
history and the terminal.

```json
{
  "auth_url": "https://keystone.example.com/v3",
  "token": "gAAAAA…",
  "region": "RegionOne",
  "project_id": "0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e",
  "project_name": "demo",
  "user_name": "alice",
  "expires_at": "2026-10-17T18:00:00Z",
  "cacert": "/etc/koc/ca.pem"
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `auth_url` | yes | Keystone v3 URL (`OS_AUTH_URL`) |
| `token` | yes | the user's Keystone token (`OS_TOKEN`, token auth) |
| `project_id` | yes | the project to scope to (`OS_PROJECT_ID`) |
| `expires_at` | yes | the token's expiry, RFC 3339 |
| `region` | no | `OS_REGION_NAME` |
| `project_name` | no | shown in the prompt and by `scope`; not sent |
| `user_name` | no | shown by `scope`; not sent |
| `cacert` | no | a CA bundle path for Keystone and the catalog endpoints (`OS_CACERT`) |

Unknown fields are rejected, so a typo cannot silently drop a setting.

- **It maps onto the same options `OS_*` sets** — token auth with `token`,
  `auth_url`, `region`, `project_id` and `cacert` — and clears every other
  credential and scope option (`OS_CLOUD`, `OS_USERNAME`, `OS_PASSWORD`, the
  domain and application-credential settings, …), so a leftover openrc in the
  environment cannot merge into the session.
- **Precedence**: flags typed on the line, then the scope file, then the `OS_*`
  environment read at start. Without `KOC_SCOPE_FILE`, the environment works as
  it always has.
- **A change** is announced in one line (`scope: RegionTwo · payments`) and
  shows in the prompt.
- **Expiry**: once `expires_at` has passed, a command line is refused with
  `the session expired at …; the console will renew it`. koc never falls back
  to password authentication. `help`, `scope` and the other built-ins still
  work.
- **A missing, unreadable, invalid or half-written file** refuses command lines
  until it parses again; it does not fall back to the environment. Write it
  atomically (to a temporary file, then `rename`).
- **The token is the only secret on disk.** It is never printed, logged or put
  in an error message, and `--debug` redacts it from the token request body and
  every `X-Auth-Token` header.

## The embedded profile

`KOC_PROFILE=embedded` — and every `koc-embedded` binary — is koc for a
console terminal: the user acts only as themselves, in the scope the host set,
in a throwaway sandbox whose disk they cannot reach from the browser. One
table in `internal/cli/embedded.go`, keyed by command path and flag name,
refuses on the line:

**Credentials and endpoints** — *"--x is not available in the console
terminal: you act as yourself, in the scope the console set"*:
`--os-token`, `--os-auth-url`, `--os-auth-type`, `--os-cloud`, `--os-username`,
`--os-user-id`, `--os-password`, `--os-password-stdin`, `--os-passcode`,
`--os-application-credential-id`, `--os-application-credential-name`,
`--os-application-credential-secret`, `--os-trust-id`, `--os-endpoint`,
`--os-system-scope`, `--os-user-domain-name`, `--os-user-domain-id`,
`--insecure`, `--keyvrm-endpoint`, and (in the default binary only, which has
them) `--creds-from-ns`, `--creds-from-vault`, `--kube-context` and the
`--vault-*` connection flags.

**Scope switches** — *"switch project (or region) in the console"*:
`--os-project-name`, `--os-tenant-name`, `--os-tenant-id`,
`--os-project-domain-name`, `--os-project-domain-id`, `--os-domain-name`,
`--os-domain-id`; and `--os-region-name` / `--os-project-id` unless the value
is the session's own (the scope file's, or the environment's).

**Local files** — *"not available in the console terminal: files stay inside
the session"*:

| Refused | Direction |
| --- | --- |
| `--os-cacert`, `--os-cert`, `--os-key` (and `--kubeconfig`, `--vault-cacert` in the default binary) | in |
| `image create --file` | in |
| `image stage` (a file or standard input) | in |
| `image save` (to a file, or raw bytes on the terminal) | out |
| `server create --user-data`, `server rebuild --user-data` | in |
| `server password show --private-key` (which also prompts on `/dev/tty`) | in |
| `keypair create --public-key` | in |
| `baremetal node inventory save --file`, `baremetal introspection data save --file` | out (without `--file` they print the JSON) |
| `zone import create <zonefile>` | in |
| `baremetal deploy template create/set --steps`, `baremetal runbook create/set --steps`, `baremetal node clean --clean-steps`, `baremetal node service --service-steps`, `baremetal inspection rule create/set --actions/--conditions` — a path or `-` | in (an inline JSON/YAML document is allowed) |

`TestEmbeddedFileFlags` walks the whole command tree and fails for any flag or
argument that looks like a local file and is not in the table, so a new one
cannot slip into the console unnoticed.

**Prompts**: the password prompt is off; `server set --root-password` is
refused (use `--password`); the key passphrase prompt is unreachable because
`--private-key` is refused. koc has no other confirmation prompt.

**Whole commands**: `koc vault` and `koc s3` (absent from a `koc-embedded`
binary altogether).

**Generated private keys**: `keypair create` without `--public-key` makes nova
generate the pair and return the private key exactly once. In the embedded
profile it needs `--print-private-key` — a flag that exists only in that
profile — and prints a warning before the key that it is shown once and kept
nowhere.

**Kept**: every output format, `--watch`, `--debug` (redacted), `--timing`,
`--timeout`, the microversion flags, `--os-interface`, and every other command.

A refused line exits 1 with the message on stderr; in the shell, it sets the
prompt's status and is an audit event with a `refused` reason.

### The `koc-embedded` binary

A build with `-tags koc_embedded` (`make embedded`) is always in the embedded
profile and leaves out the Vault and S3 command groups and the Kubernetes
integration entirely — no code, no flags, no help lines; `make embedded-check`
fails if its package graph reaches `internal/kube`, `internal/vault` or
`internal/s3`. Releases ship it as
`koc-embedded_<version>_linux_{amd64,arm64}.tar.gz`, listed in `checksums.txt`
like every other archive; it is not in the `.rpm`/`.deb` or the Homebrew cask.
The sandbox image is built elsewhere from that archive.

## Audit events

With `KOC_AUDIT_FD` or `KOC_AUDIT_FILE` set, the shell writes one JSON object
and a newline per line it handles — commands, built-ins and refused lines; not
empty lines or comments. The command's output is never part of an event.

```json
{"ts":"2026-10-17T17:13:28.692709621Z","argv":["server","list","--os-password","***","--long"],"command":"server list","region":"RegionOne","project_id":"0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e","exit_code":0,"duration_ms":412}
{"ts":"2026-10-17T17:13:31.120083514Z","argv":["image","save","cirros"],"command":"image save","region":"RegionOne","project_id":"0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e","exit_code":1,"duration_ms":3,"refused":"file"}
```

| Field | Meaning |
| --- | --- |
| `ts` | when the line started, RFC 3339 in UTC |
| `argv` | the arguments after abbreviation expansion and prefix stripping, with every secret flag's value replaced by `"***"` |
| `command` | the resolved command path (`server list`), or the built-in's name |
| `region`, `project_id` | the scope the line ran in (the line's own matching flags included) |
| `exit_code` | the line's status, as the prompt shows it |
| `duration_ms` | wall-clock time of the line |
| `refused` | present only for a refused line: `syntax`, `nested` (`shell` inside the shell), `expired`, `scope` (scope file unusable), or the embedded profile's `credential`, `scope`, `file`, `prompt`, `command`, `private-key` |

A secret is recognised by flag name with the same key list `--debug` redacts
response bodies with (`password`, `token`, `secret`, `passcode`, `admin_pass`,
`private_key`, `secret_id`, `role_id`, …), looked up on the resolved command
so a boolean flag never swallows the next argument. Fields may be added; none
will be renamed.

If the descriptor or file cannot be opened or written, koc warns once on stderr
and carries on without it. Refusing to run unaudited is the host's decision:
watch your end of the pipe and stop the sandbox when it closes.

## Wiring it up (host side)

A sketch of what a host does per browser session:

1. Start a sandbox from an image holding `koc-embedded`, with a read-only root,
   a tmpfs `KOC_HOME`, and egress only to that region's OpenStack APIs.
2. Write the scope file into the sandbox (atomically), set `KOC_SCOPE_FILE`,
   and open a pipe for audit events as fd 3 with `KOC_AUDIT_FD=3`.
3. Run `koc-embedded shell` on a PTY and bridge it to the browser's WebSocket.
   Pass terminal resizes through to the PTY; koc reads the size before every
   line.
4. Rewrite the scope file to rotate the token before `expires_at`, or to
   switch region or project when the user does so in the console.
5. Optionally mount a per-user `KOC_HISTORY_FILE` to keep history across
   sessions.
