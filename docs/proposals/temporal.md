# Proposal: `koc temporal …`, operator access to the LCM Temporal

Status: design proposal, not implemented. Every choice below has a stated
default; the open questions at the end can override them before coding starts.

## Why

The LCM control plane runs a Temporal cluster (server 1.32.x) for its own
workflows and for the services that move onto it (bare-metal enrolment,
Kubernetes-as-a-service, later KeyVRM). It is deliberately internal:

- the frontend is a ClusterIP service, reachable only from inside the LCM
  Kubernetes cluster and only from the namespaces its NetworkPolicy admits;
- its frontend requires mutual TLS: a client certificate issued by the
  cluster's internal CA;
- there is no Web UI.

Operators still have to look at it: why is enrolment of a node stuck, which
schedule last ran, does a task queue have pollers, cancel or terminate a
workflow that will never finish. Today that means `kubectl exec` into the
admin-tools pod and running the upstream `temporal` CLI there, with
cluster-admin rights and no record of who did what.

The requirement: **human access to the LCM Temporal goes through koc**, with
the same login, permissions, output formats and audit as the rest of the LCM.

## Recommendation in one screen

1. **Transport: koc does not dial Temporal.** lcm-core (the LCM API that the
   console, koc and automation already call) gets a small REST group,
   `/api/v1/temporal/…`. lcm-core authenticates the user, checks a
   `temporal.*` permission on the Temporal namespace, then calls Temporal with
   its own client certificate and the Go SDK. koc calls that REST group like
   any other lcm-core API. No port-forward, no client certificates for
   people, no gRPC in koc.
2. **Login: OIDC device authorization grant** (RFC 8628) against the LCM
   identity provider (Authentik), through a public OIDC client `koc`.
   `koc lcm login` caches the tokens under `KOC_HOME` and refreshes them.
   Until lcm-core's OIDC login lands, a static lcm-core bearer token
   (`KOC_LCM_TOKEN`) works with the same commands.
3. **Authorization lives in lcm-core, per Temporal namespace**: permissions
   `temporal.<resource>.<verb>` on a namespace resource, three predefined
   roles (viewer, operator, admin). Tokens carry identity only; Temporal's own
   JWT `permissions` claims are not used.
4. **Payloads are hidden by default.** Workflow inputs, results, signal
   arguments and failure details can carry secrets; showing them needs
   `--payloads` and the separate permission `temporal.payloads.get`.
5. **Commands**: namespaces, workflows (list, show, history, count, cancel,
   terminate, signal), schedules (list, show, pause, unpause, trigger), task
   queues (show), cluster (show, health): 17 leaves under `koc temporal`, plus
   `koc lcm login|logout|whoami`. Every read verb gets `--watch` and `-f
   table|json|yaml|value|csv|tsv` for free, like every other koc command.
6. **SDK**: `go.temporal.io/sdk` v1.50.0 / `go.temporal.io/api` v1.63.x in
   lcm-core (the versions lcm-core already pins), against server 1.32.x. koc
   vendors no Temporal or gRPC code.
7. **Effort**: koc about 7 days, lcm-core about 6 days, IdP client and stand
   end-to-end about 1.5 days: **about 14-15 developer days** in total.

## What koc talks to

```
 operator laptop / jump host                    LCM Kubernetes cluster
 ┌────────────────────┐   HTTPS + Bearer   ┌──────────────────────────────┐
 │ koc temporal …     │ ─────────────────▶ │ ingress (operator VIP)       │
 │ koc lcm login      │                    │   └▶ lcm-core /api/v1/temporal│
 └─────────┬──────────┘                    │        │ authn: OIDC token    │
           │ device flow                   │        │ authz: temporal.*    │
           ▼                               │        │ gRPC + mTLS (SDK)    │
 ┌────────────────────┐                    │        ▼                     │
 │ IdP (Authentik)    │ ◀── JWKS ───────── │   temporal-frontend :7233    │
 └────────────────────┘                    │   (ClusterIP, NetworkPolicy) │
                                           └──────────────────────────────┘
```

## How koc reaches the ClusterIP frontend

| | A. lcm-core REST group (recommended) | B. kube port-forward | C. exposed frontend (TLS passthrough LB) |
|---|---|---|---|
| Path | koc → ingress → lcm-core → Temporal | koc → kube-apiserver → pod :7233 | koc → LB IP :7233 → frontend |
| Who can use it | anyone with an LCM login and a `temporal.*` role | holders of a kubeconfig with `pods/portforward` in the Temporal namespace (cluster admins) | anyone holding a client certificate from the cluster CA |
| Human credential | OIDC token (15 min, refreshed) | kubeconfig + a client certificate for Temporal | client certificate per person + a JWT |
| Per-namespace RBAC | lcm-core IAM, the same model as every other LCM permission | none (the Temporal server cannot map a certificate to a namespace, see below) | Temporal's JWT authorizer with `permissions` claims in the token |
| Audit | lcm-core audit log + Temporal `identity` on every write | kube audit of the port-forward only | Temporal principal only |
| koc changes | REST client + DTOs (the KeyVRM/Vault pattern) | port-forward over the kube API (SPDY or WebSocket tunnel) + gRPC + Temporal SDK | gRPC + Temporal SDK |
| koc binary | +0 MB of dependencies | about +14 MB (measured, below) | about +14 MB |
| Server changes | one client certificate for lcm-core; NetworkPolicy admits lcm-core | none | an LB IP, TLS passthrough, `authorizer: default`, JWT claim mapper, internal-frontend |
| Fits "tokens carry identity, not permissions" | yes | n/a | no |

**A** is recommended. It keeps the frontend unpublished, gives people one
credential (their LCM login), puts Temporal access under the same permission
model and audit as the rest of the LCM, and keeps koc a plain HTTPS client
with no new dependency tree.

**B** stays what it is today, a break-glass path for cluster admins, and needs
no koc work: `kubectl -n <temporal-ns> exec deploy/<admin-tools> -- temporal …`
uses the upstream CLI that ships in the admin-tools image. It is documented in
the operator runbook, not in koc.

**C** is the path for *out-of-cluster workers* (a region service that must
poll task queues from outside the LCM cluster). It is not a human access path
and is not needed for this feature.

### The Temporal server facts behind this (read in the 1.32.0 source)

- `common/authorization/claim_mapper.go`, `GetClaimMapperFromConfig`: the only
  configurable claim mappers are `""` (noop: **every caller with a valid
  client certificate is system admin**) and `"default"` (JWT from the
  `authorization` header, with roles from a `permissions` claim of
  `"<namespace>:<read|write|worker|admin>"` strings, or from a custom
  `permissionsRegex`).
- There is no configuration that maps a TLS subject to namespaces. That needs
  a custom server build (`temporal.WithClaimMapper`).
- `default_authorizer.go`: with the default authorizer, a caller with no token
  gets no roles and is denied everything except the health check, so turning
  it on for people also forces every worker onto JWTs.

So with option A the server keeps its first-cut configuration (mTLS required,
noop claim mapper), lcm-core's certificate is one more client certificate,
and the per-person, per-namespace decision is made in lcm-core.

## Authentication

### People: `koc lcm login`

```
$ koc lcm login --lcm-url https://lcm.example.com
To sign in, open https://auth.example.com/device and enter the code WDJB-MJHT
(or open https://auth.example.com/device?code=WDJB-MJHT)
Waiting for sign-in… done.
Signed in to https://lcm.example.com as alice (expires 2026-11-02T10:15:00Z).
```

1. koc reads `GET /api/v1/auth/cli` from lcm-core (no auth): `{"issuer":
   "...", "client_id": "koc", "scopes": ["openid", "profile", "email",
   "offline_access"]}`. The user types only the LCM URL.
2. OIDC discovery on the issuer (`/.well-known/openid-configuration`) gives
   the device authorization, token and revocation endpoints.
3. Device authorization grant (RFC 8628): print the verification URI and user
   code to stderr, poll the token endpoint at the server's `interval`, honour
   `slow_down`, stop on `access_denied` or `expired_token`. It works over SSH
   on a jump host with no browser, which is why it is the default.
4. `--browser`: authorization code + PKCE with a loopback redirect
   (`http://127.0.0.1:<random port>/callback`) for a desktop session.
5. The access token (15 minutes) and refresh token go to
   `$KOC_HOME/lcm/credentials.json` (or the user's config dir without
   `KOC_HOME`), mode 0600, one entry per LCM URL. Every command refreshes the
   access token when less than 60 s remain. A failed refresh says
   `session expired: run koc lcm login`.
6. `koc lcm logout` revokes the refresh token at the revocation endpoint and
   deletes the entry. `koc lcm whoami` shows URL, subject, name, expiry; never
   the token.

The IdP needs one public client `koc` with the grant types device code,
authorization code (loopback redirect URIs, PKCE required) and refresh token,
and the scope mappings openid, profile, email and offline_access. lcm-core
accepts tokens whose audience is `koc` as well as its own.

### Automation and the interim

- `KOC_LCM_TOKEN` / `--lcm-token` sends a bearer token as is (an IdP
  service-account token, or one of lcm-core's static role tokens while its
  OIDC login is not built). It wins over the cached login.
- `KOC_LCM_URL` / `--lcm-url` set the API. On an LCM cluster node, with
  neither set, koc reads the URL from the LCM config map it already reads for
  Vault discovery (the key is to be added there).
- `KOC_LCM_CACERT` / `--lcm-cacert` for a private CA; system roots otherwise.
  `--insecure` keeps its global meaning.
- lcm-core's `GET /api/v1/capabilities` tells koc whether this LCM serves the
  Temporal group. If not, koc answers `this LCM has no Temporal API
  (capability "temporal" absent)` instead of a 404.

### The console terminal (embedded profile)

- The scope file gains optional `lcm_url`, `lcm_token` and `lcm_expires_at`:
  the host hands over the user's own LCM token, as it does for Keystone.
- `koc lcm login` and `koc lcm logout` are refused there ("you act as yourself,
  in the scope the console set"), and `--lcm-url`, `--lcm-token`,
  `--lcm-cacert` join the credential refusals in `internal/cli/embedded.go`.
- `koc temporal workflow signal --input-file` is a file flag and joins the file
  refusals; `--input` (inline JSON) stays allowed.

### Secrets in transit and in logs

- `--debug` redacts `Authorization: Bearer …`, and the form fields
  `refresh_token`, `device_code`, `code` and `code_verifier` of token-endpoint
  bodies, alongside the existing redactions.
- The shell's audit events replace the value of `--lcm-token` and `--input`
  with `"***"` (a signal argument can be a secret).

## Authorization: permissions per Temporal namespace

A Temporal namespace is a resource in lcm-core's infrastructure hierarchy
(`temporal/namespaces/<name>`); a role binding on it, or on its parent (all
namespaces), grants the permissions below. lcm-core checks the permission on
the namespace named in the path, and filters `namespace list` to the
namespaces the caller can read.

| Permission | Operations |
|---|---|
| `temporal.namespaces.list` | namespace list |
| `temporal.namespaces.get` | namespace show |
| `temporal.workflows.list` | workflow list, workflow count |
| `temporal.workflows.get` | workflow show, workflow history (metadata only) |
| `temporal.payloads.get` | `--payloads` on workflow show/history: decoded inputs, results, signal arguments, memo, failure details |
| `temporal.workflows.cancel` | workflow cancel |
| `temporal.workflows.signal` | workflow signal |
| `temporal.workflows.terminate` | workflow terminate |
| `temporal.schedules.list` / `.get` | schedule list / show |
| `temporal.schedules.pause` | schedule pause, schedule unpause |
| `temporal.schedules.trigger` | schedule trigger |
| `temporal.taskQueues.get` | task-queue show |
| `temporal.cluster.get` | cluster show, cluster health |

Predefined roles:

| Role | Grants |
|---|---|
| `temporal.viewer` | every `list`/`get` above except `temporal.payloads.get` and `temporal.cluster.get` |
| `temporal.operator` | viewer + `workflows.cancel`, `workflows.signal`, `schedules.pause`, `schedules.trigger` |
| `temporal.admin` | operator + `workflows.terminate`, `payloads.get`, `cluster.get` |

Typical bindings: the platform team gets `temporal.admin` on all namespaces; a
service team gets `temporal.operator` on its own namespace only. A denial is a
403 problem document naming the permission and namespace, and koc prints it
as `permission denied: temporal.workflows.terminate on namespace "x"`.

Every write that lcm-core makes on a user's behalf sets the Temporal request's
`identity` to `lcm-core:<user subject>`, so the workflow history itself
records who cancelled, signalled or terminated it, in addition to lcm-core's
audit log.

## Command tree

Nouns and verbs follow koc's OSC style (`list`, `show`, `set`-style verbs);
the upstream `temporal` CLI is the reference for semantics, not for names.

| koc | upstream `temporal` CLI | lcm-core route | Permission | `--watch` |
|---|---|---|---|---|
| `temporal namespace list` | `operator namespace list` | `GET /api/v1/temporal/namespaces` | namespaces.list | yes |
| `temporal namespace show <ns>` | `operator namespace describe` | `GET …/namespaces/{ns}` | namespaces.get | yes |
| `temporal workflow list` | `workflow list` | `GET …/namespaces/{ns}/workflows` | workflows.list | yes |
| `temporal workflow count` | `workflow count` | `GET …/namespaces/{ns}/workflows:count` | workflows.list | yes |
| `temporal workflow show <id>` | `workflow describe` | `GET …/workflows/{id}` | workflows.get | yes |
| `temporal workflow history <id>` | `workflow show` | `GET …/workflows/{id}/history` | workflows.get | yes (not with `--follow`) |
| `temporal workflow cancel <id>` | `workflow cancel` | `POST …/workflows/{id}:cancel` | workflows.cancel | no |
| `temporal workflow terminate <id>` | `workflow terminate` | `POST …/workflows/{id}:terminate` | workflows.terminate | no |
| `temporal workflow signal <id>` | `workflow signal` | `POST …/workflows/{id}:signal` | workflows.signal | no |
| `temporal schedule list` | `schedule list` | `GET …/namespaces/{ns}/schedules` | schedules.list | yes |
| `temporal schedule show <id>` | `schedule describe` | `GET …/schedules/{id}` | schedules.get | yes |
| `temporal schedule pause <id>` | `schedule toggle --pause` | `POST …/schedules/{id}:pause` | schedules.pause | no |
| `temporal schedule unpause <id>` | `schedule toggle --unpause` | `POST …/schedules/{id}:unpause` | schedules.pause | no |
| `temporal schedule trigger <id>` | `schedule trigger` | `POST …/schedules/{id}:trigger` | schedules.trigger | no |
| `temporal task-queue show <name>` | `task-queue describe` | `GET …/namespaces/{ns}/task-queues/{name}` | taskQueues.get | yes |
| `temporal cluster show` | `operator cluster describe` + `system` | `GET /api/v1/temporal/cluster` | cluster.get | yes |
| `temporal cluster health` | `operator cluster health` | `GET /api/v1/temporal/cluster/health` | cluster.get | yes |
| `lcm login` / `lcm logout` / `lcm whoami` | (none) | IdP endpoints; `GET /api/v1/auth/cli` | none | no |

`workflow show` vs `workflow history` deviate from the upstream names on
purpose: in koc `show` always means "describe one resource". Both go into the
"Naming deviations" table of `docs/coverage.md`; the whole group goes under
"koc-native commands" (there is no OSC equivalent).

### Flags

Common to every `koc temporal` leaf:

- `--namespace <ns>` (env `KOC_TEMPORAL_NAMESPACE`). **Required** unless the
  env is set: there is no default namespace, because a terminate in the wrong
  one is expensive. The error lists the namespaces the caller can read.
- `--lcm-url`, `--lcm-token`, `--lcm-cacert` (above); the global output,
  `--debug`, `--timeout` and `--insecure` flags as everywhere.

Per leaf:

| Leaf | Flags |
|---|---|
| workflow list | `--query <visibility query>` (Temporal's SQL-like list filter, passed through); shortcuts `--status running\|completed\|failed\|canceled\|terminated\|continued-as-new\|timed-out` (repeatable), `--type <workflow type>`, `--task-queue <name>`, `--started-after <RFC 3339 or 1h/2d>`; they AND with `--query`; `--limit N` (default 100, a hard cap), `--all` (no cap), `--long` |
| workflow count | the same filters; `--group-by ExecutionStatus` (the one grouping Temporal supports) |
| workflow show | `--run-id`; `--payloads` |
| workflow history | `--run-id`; `--event-type <type>` (repeatable filter, client side); `--last N`; `--follow` (long-poll until the workflow closes; refuses `--watch`); `--payloads`; `--long` |
| workflow cancel | `--run-id`; `--reason <text>` |
| workflow terminate | `--run-id`; `--reason <text>` (**required**, recorded in history) |
| workflow signal | `--name <signal>` (required); `--input <json>` or `--input-file <path>` (one JSON value, or a JSON array with `--multi-arg`); `--run-id` |
| schedule list | `--limit`, `--all`, `--long` |
| schedule show | `--payloads` (the action's workflow input) |
| schedule pause / unpause | `--note <text>` |
| schedule trigger | `--overlap-policy skip\|buffer-one\|buffer-all\|cancel-other\|terminate-other\|allow-all` (default: the schedule's own) |
| task-queue show | `--type workflow\|activity\|all` (default all); `--long` (one row per poller) |

No verb prompts for confirmation, as no koc delete does; `terminate` is
guarded by its required `--reason` and by the admin-only permission.

Workflow and schedule IDs are free-form strings and often contain `/`. koc
escapes each path segment (`url.PathEscape`); lcm-core routes on the raw path.

## Output

All rendering goes through `internal/output`; no inline formatting. Times are
RFC 3339 in UTC, as elsewhere in koc; durations in Go notation (`1h2m3s`).

| Leaf | Default columns (table) | `--long` adds |
|---|---|---|
| namespace list | Name, State, Retention, Description | ID, Owner Email |
| namespace show | Name, ID, State, Description, Owner Email, Retention, History Archival, Visibility Archival, Is Global, Data | |
| workflow list | Workflow ID, Type, Status, Start Time | Run ID, Task Queue, Close Time, History Length, Parent Workflow ID |
| workflow count | Count (or Status, Count with `--group-by`) | |
| workflow show | Workflow ID, Run ID, Type, Status, Task Queue, Start Time, Execution Time, Close Time, History Length, History Size, Parent, Root, Pending Activities (type, attempt, last failure message, last heartbeat), Pending Children, Search Attributes, Memo (keys only without `--payloads`), Last Failure (type and message) | |
| workflow history | Event ID, Time, Type, Summary | Task Queue, Attempt, Identity |
| schedule list | Schedule ID, Workflow Type, Spec, Paused, Next Run | Last Run, Last Result, Note |
| schedule show | Schedule ID, Spec (cron/interval/calendar), Timezone, Paused, Note, Overlap Policy, Catchup Window, Workflow Type, Workflow ID prefix, Task Queue, Recent Actions, Next Runs (5), Created, Updated | |
| task-queue show | Type, Pollers, Last Poll, Backlog (approx.), Backlog Age, Add Rate, Dispatch Rate | per poller: Identity, Last Access, Rate per Second |
| cluster show | Cluster Name, Cluster ID, Server Version, History Shards, Persistence Store, Visibility Store, Capabilities | |
| cluster health | Service, Status (`SERVING` / `NOT_SERVING`) | |

- `Summary` in the history is a one-line digest per event type
  (`ActivityTaskScheduled: enroll_node on queue baremetal, attempt 3`,
  `WorkflowExecutionTerminated: reason "stuck", by lcm-core:alice`).
- **Payloads.** Without `--payloads` every payload field renders as
  `<json, 312 bytes>` (encoding and size). With it, lcm-core decodes
  `json/plain` payloads to JSON values; any other encoding (binary,
  protobuf, an encrypting codec) stays `<encoding, size>`. `-f json` carries the
  same: a decoded value, or `{"encoding": "...", "size": N}`.
- `-f json|yaml` emit koc-owned DTOs (snake_case, as lcm-core's API), not raw
  Temporal protobuf JSON, so scripts do not break on a Temporal upgrade.
- Exit codes: 0 on success; non-zero with a one-line stderr message otherwise
  (`workflow "x" not found in namespace "y"`, `workflow "x" already
  completed`, `permission denied: …`, `session expired: run koc lcm login`).

## The lcm-core contract koc consumes

The group follows lcm-core's API rules: version in the path, RFC 9457 problem
documents with a stable `code`, cursor pagination (`limit` 1-500, `cursor`,
`next_cursor`), custom methods as `POST …/{id}:verb`, `X-Request-Id`, strong
`ETag` on single resources (so `--watch` polls cheaply).

| Route | Temporal call (SDK) |
|---|---|
| `GET /api/v1/temporal/cluster` | `GetClusterInfo`, `GetSystemInfo` |
| `GET /api/v1/temporal/cluster/health` | `CheckHealth` (gRPC health of the frontend) |
| `GET /api/v1/temporal/namespaces[/{ns}]` | `ListNamespaces` / `DescribeNamespace` |
| `GET …/{ns}/workflows?query=&limit=&cursor=` | `ListWorkflowExecutions` |
| `GET …/{ns}/workflows:count?query=&group_by=` | `CountWorkflowExecutions` |
| `GET …/{ns}/workflows/{id}?run_id=&payloads=` | `DescribeWorkflowExecution` |
| `GET …/{ns}/workflows/{id}/history?run_id=&cursor=&wait=&payloads=` | `GetWorkflowExecutionHistory` (`wait=true`: long poll, capped at 30 s per call) |
| `POST …/{ns}/workflows/{id}:cancel` `{run_id, reason}` | `RequestCancelWorkflowExecution` |
| `POST …/{ns}/workflows/{id}:terminate` `{run_id, reason}` | `TerminateWorkflowExecution` |
| `POST …/{ns}/workflows/{id}:signal` `{run_id, name, input}` | `SignalWorkflowExecution` (`input` encoded as `json/plain` payloads) |
| `GET …/{ns}/schedules[/{id}]` | `ListSchedules` / `DescribeSchedule` |
| `POST …/{ns}/schedules/{id}:pause\|:unpause\|:trigger` | `PatchSchedule` |
| `GET …/{ns}/task-queues/{name}?type=` | `DescribeTaskQueue` with `report_stats` and pollers |

- lcm-core calls the raw `WorkflowService()` client so that every request can
  carry the caller's `identity`; koc's `Idempotency-Key` becomes the Temporal
  `request_id` on cancel, signal and schedule patches, so a retried command
  does not signal twice.
- Writes answer `200` with the resulting state (they are synchronous in
  Temporal and start no lcm-core Operation).
- Problem codes koc maps: `temporal.not_found`, `temporal.already_completed`,
  `temporal.invalid_query`, `permission_denied`, `temporal.unavailable`
  (frontend down; `cluster health` still answers, with `NOT_SERVING`).
- lcm-core limits: page size at most 500; history pages at most 1000 events;
  decoded payloads larger than 64 KiB are cut with a marker.

## Go SDK and versions

- **lcm-core** uses `go.temporal.io/sdk` **v1.50.0** and `go.temporal.io/api`
  v1.63.x, the versions it already pins for its worker. Both require Go
  1.26, as koc and lcm-core do. They talk to server **1.32.x**; the SDK and the
  server are pinned together per platform release and moved together.
- **koc** vendors neither. Measured on a minimal program (static,
  `-trimpath -ldflags "-s -w"`, linux/amd64): a TLS `net/http` client is
  5.9 MB, the same plus `go.temporal.io/sdk/client` v1.50.0 is 20.0 MB
  (**+14 MB**), pulling in gRPC, protobuf, gogo/protobuf, the Nexus RPC SDK,
  grpc-ecosystem middleware and robfig/cron (121 `go.sum` lines). koc keeps
  its minimal-REST pattern (as for Vault, S3 and Kubernetes) and its
  dependency list.
- If option B or C is ever chosen, the SDK goes into koc behind a
  `pkg/temporal` seam and this section is revisited.

## Package layout in koc

```
pkg/lcm/                   lcm-core client: base URL, bearer, CA, X-Request-Id,
                           problem+json errors, cursor paging (Collect)
pkg/lcm/temporal/          typed requests + DTOs for /api/v1/temporal (public API)
internal/oidc/             discovery, device grant, loopback PKCE, refresh, revoke
internal/lcmauth/          credentials file under KOC_HOME, token selection order
                           (flag > env > scope file > cached login), capability probe
internal/lcm/lcmtest/      fake lcm-core (Temporal group + auth/cli) with TestContract
internal/oidc/oidctest/    fake IdP (device grant, PKCE, refresh, revoke)
internal/cli/lcm/          koc lcm login|logout|whoami
internal/cli/temporal/     one file per noun: namespace.go, workflow.go,
                           schedule.go, taskqueue.go, cluster.go; client.go helper
```

Seams follow the command pattern: `runWorkflowList(ctx, c *lcm.Client, o
*output.Options, opts, w io.Writer)`, with the API half public in
`pkg/lcm/temporal`.

## Tests

- **Unit, through the seams** against `lcmtest`: method, path (including
  workflow IDs with `/`, `%`, spaces), query string, body, `Idempotency-Key`,
  and the rendered output in every format; paging and `--limit`/`--all`;
  every problem code mapped to its message; payloads hidden and shown.
- **OIDC** against `oidctest`: `authorization_pending`, `slow_down`,
  `expired_token`, `access_denied`; refresh before expiry; refresh failure;
  revoke on logout; credentials file mode 0600 and confinement to `KOC_HOME`.
- **Embedded profile**: the new credential and file flags are refused
  (`TestEmbeddedFileFlags` covers `--input-file`); scope-file fields
  `lcm_url`/`lcm_token`/`lcm_expires_at` parse, and unknown fields still fail.
- **Redaction**: `--debug` hides the bearer and the token-endpoint form
  fields; audit events hide `--lcm-token` and `--input`.
- **Contract**: `lcmtest.TestContract` re-runs its table against a real
  lcm-core when `LCMTEST_ADDR` and `LCMTEST_TOKEN` are set, so the fake stays
  pinned to real answers (the `vaulttest` pattern).
- **Functional** (`test/functional`): every new leaf is invoked, so
  `TestEveryLeafIsCovered` passes. Offline CI runs them against `lcmtest`;
  with `KOC_FT_LCM_ENV` set they run against a real LCM: a test workflow
  started in a test namespace by the harness with the upstream CLI, then
  koc lists, shows, follows its history, signals, cancels a second one and
  terminates a third; schedules are created by the harness and paused,
  unpaused and triggered by koc.
- **lcm-core side** (in its own repo): handler tests against the SDK's
  development server, and an authorization table test (each route × each
  predefined role → allow/deny).

## Effort and staging

| Item | Days |
|---|---|
| koc: `pkg/lcm` client, credential selection, capability probe | 1 |
| koc: OIDC device grant, loopback PKCE, refresh, revoke; `lcm login/logout/whoami` | 1.5 |
| koc: `pkg/lcm/temporal` DTOs and the 17 `temporal` leaves | 2.5 |
| koc: fakes, unit, functional, embedded and redaction tests | 1.5 |
| koc: docs (`coverage.md`, README, `shell.md` scope-file fields) | 0.5 |
| **koc subtotal** | **7** |
| lcm-core: the `/api/v1/temporal` group (OpenAPI with permissions, handlers over the SDK, identity, payload decoding) | 3 |
| lcm-core: authorization (predefined roles; static role tokens now, the IAM decision point later) | 1 |
| lcm-core: client certificate, Temporal connection config, NetworkPolicy admission | 0.5 |
| lcm-core: tests | 1.5 |
| **lcm-core subtotal** | **6** |
| IdP: the public `koc` client (device code, PKCE, refresh) | 0.5 |
| End-to-end on an LCM test stand | 1 |
| **Total** | **14.5** |

Stages, each usable on its own:

0. **Now, no code:** the break-glass runbook (`kubectl exec` into admin-tools,
   upstream CLI).
1. **lcm-core group + koc commands with `KOC_LCM_TOKEN`** (static role tokens):
   read verbs first, then writes.
2. **`koc lcm login`** once lcm-core accepts OIDC tokens and the IdP client
   exists.
3. **The IAM decision point** behind lcm-core's authorizer, same permission
   names; no koc change.

Dependencies: the Temporal release in the LCM bundle, lcm-core deployed in the
LCM cluster, the IdP on the LCM, lcm-core's OIDC login (stage 2 only).

## Not in the first release

- `workflow start`, `query`, `stack`, `reset`, `update`, batch operations by
  query (terminate or cancel many), `workflow trace`.
- Schedule create, update, delete, backfill: the workers own their schedules
  and re-create them at start.
- Namespace create, update, delete: the installer owns namespaces.
- Search-attribute management, Nexus endpoints, worker deployments and
  versioning, task-queue partitions.
- A Web UI.

`reset` and batch terminate are the likely next ones, behind
`temporal.admin`.

## Risks

- **lcm-core's client certificate is a system-admin identity on Temporal**
  (noop claim mapper). The blast radius is lcm-core itself: its permission
  check is the only per-person gate. Mitigation: route allowlist (the table
  above, nothing generic), the authorization table test, and lcm-core's audit
  log. Narrowing it needs the server's JWT authorizer or a custom claim
  mapper, which is a server-side decision.
- **Payload leaks.** Decoded inputs can hold credentials. Hidden by default,
  separate permission, size cap; never logged by lcm-core.
- **Visibility queries** are passed through to Temporal's SQL visibility. A
  heavy query (no time bound on a large namespace) loads the database.
  lcm-core caps page size and query time (10 s); `--started-after` is
  suggested in the error when a query times out.
- **Version drift.** Temporal adds fields every minor; koc-owned DTOs isolate
  scripts from that, and lcm-core maps new enum values to `UNKNOWN(<n>)`
  rather than failing.
- **Long polls through the ingress** (`--follow`): each call is capped at
  30 s and koc re-issues it, so idle timeouts on the path do not matter.

## Decisions (defaults chosen; override before coding if you disagree)

1. Transport A (lcm-core REST group); B is the break-glass runbook only.
2. Device authorization grant by default, loopback PKCE with `--browser`.
3. Permissions in lcm-core per namespace; Temporal's JWT `permissions` claims
   unused; the server keeps mTLS with the noop claim mapper.
4. Payloads hidden by default; `--payloads` + `temporal.payloads.get`
   (admin role).
5. Write verbs: cancel, terminate (reason required), signal, schedule
   pause/unpause/trigger. Nothing else.
6. `--namespace` required (or `KOC_TEMPORAL_NAMESPACE`); no default.
7. `workflow list` capped at 100 by default; `--all` lifts it.
8. No Temporal SDK or gRPC in koc.

## Open questions

1. Transport A as recommended, or should koc dial Temporal itself (B or C)?
2. Per-worker namespace isolation on the server side: accept none in the first
   cut (any valid client certificate is system admin), move workers to JWTs,
   or build a server image with a certificate-subject claim mapper?
3. Payloads hidden by default, admin-only `--payloads`: confirmed?
4. The write-verb set for the first release: confirmed?
5. Who adds the IdP's public `koc` client: the team that owns the IdP
   deployment, or this feature's follow-up?
