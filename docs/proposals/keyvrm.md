# Proposal: add KeyVRM support to koc (`koc keyvrm …`)

Status: IMPLEMENTED on this branch (`internal/cli/keyvrm/`), validated end-to-end
against the live cluster `llm-sl972-k0s-rc10`.

Validated live (auth via `--creds-from-vault` → Keystone token): `keyvrm
app-config show`, `availability-zone list`, `host-aggregate-config list`,
`recommendation list` all return real data through the catalog-resolved endpoint
(`type keyvrm` → `…:8001`). Decisions below were taken as recommended: namespaced
under `koc keyvrm`, OSC-aligned verbs, server-side pagination pass-through,
endpoint override via `--keyvrm-endpoint` / `OS_KEYVRM_ENDPOINT_OVERRIDE`, and the
full app-config flag set.

Known discrepancy: `host-aggregate-config markers` maps to the SDK path
`GET /v1/host_aggregates/markers`, but the **deployed** KeyVRM service has no such
route (only list, `/{id}`, `/{id}/events`), so it 422s there. This is a
`keyvrm-sdk` ↔ service version mismatch (the `kvrm` CLI hits the same path); koc
mirrors the SDK and will work once the service exposes the route.

---

Status (original): design proposal
Context: KeyVRM (Keystack Virtual Resource Manager) — an in-house OpenStack service
registered in the Keystone service catalog. Reference implementations:
`~/code/project_k/keyvrm` (service + `keyvrm_sdk`) and `~/code/project_k/keyvrm-cli`
(the Python `kvrm` CLI).

## What KeyVRM is (from the Python reference)

- A REST service **in the Keystone catalog** under service type **`keyvrm`**,
  interface `public` (both configurable). Auth is a **standard Keystone token**
  sent as `X-Auth-Token` — the same tokens koc already issues.
- The Python stack splits **SDK** (`keyvrm-sdk`: httpx client + pydantic DTOs,
  paths under `/v1/…`) from **CLI** (`keyvrm-cli`: click groups + tabulate).
- Endpoint discovery: the SDK `base_url` is the catalog endpoint for `keyvrm`;
  `KEYVRM_ENDPOINT_OVERRIDE` bypasses the catalog for local dev.

Because auth is a plain Keystone token, **KeyVRM drops straight into koc's
existing auth flow** — including `--creds-from-vault` (openrc → Keystone). No new
auth work.

## REST API (extracted from `keyvrm_sdk/api/v1`)

All paths are relative to the catalog endpoint; version prefix is `v1`. Bodies
send only non-null fields (`exclude_none`). List responses are
`{data, total, limit, offset}` (events add `last_event`,
`last_event_recommendations`).

| Resource | Method + path | Query / body | Returns |
|---|---|---|---|
| App config | `GET /v1/app_config` | — | `AppConfigDTO` |
| App config | `PUT /v1/app_config` | AppConfigUpdate | `AppConfigDTO` |
| Host aggregate cfg | `GET /v1/host_aggregates` | `availability_zone_name`, `host_aggregate_name`, `marker`, `no_op_mode`, `limit`, `offset` | list of `HostAggregateConfigDTO` |
| Host aggregate cfg | `GET /v1/host_aggregates/markers` | — | `[]string` |
| Host aggregate cfg | `GET /v1/host_aggregates/{id}` | — | `HostAggregateConfigDTO` |
| Host aggregate cfg | `PUT /v1/host_aggregates/{id}` | HostAggregateConfigUpdate (also carries `no_op_mode`/`no_op_mode_reason`) | `HostAggregateConfigDTO` |
| Host aggregate cfg | `GET /v1/host_aggregates/{id}/events` | `status`, `limit`, `offset` | list of `HostAggregateEventDTO` |
| Availability zone | `GET /v1/azones` | `limit`, `offset` | list of `AZoneDTO` |
| Availability zone | `GET /v1/azones/{az_name}/host_aggregates` | `host_aggregate_name`, `no_op_mode`, `limit`, `offset` | `AZoneHostAggregatesDTO` |
| Event | `GET /v1/host_aggregate_events/{id}` | — | `HostAggregateEventDTO` |
| Event | `GET /v1/host_aggregate_events/{id}/recommendations` | `status`, `limit`, `offset` | list of `RecommendationDTO` |
| Event | `POST /v1/host_aggregate_events/{id}/recommendations/run` | — | (async trigger) |
| Recommendation | `GET /v1/recommendations` | `host_aggregate_event_id`, `status`, `limit`, `offset` | list of `RecommendationDTO` |
| Recommendation | `GET /v1/recommendations/{id}` | — | `RecommendationDTO` |
| Recommendation | `GET /v1/recommendations/{id}/operations` | `status`, `limit`, `offset` | list of `OperationDTO` |
| Recommendation | `POST /v1/recommendations/{id}/run` | — | (async trigger) |
| Recommendation | `POST /v1/recommendations/{id}/stop` | — | (async trigger) |

Key DTO fields (for Go structs / table columns):

- **AppConfigDTO**: `enabled`, `period`, `nova_enabled_filters`,
  `ha_preserve_ephemeral_device`, `ha_evacuate_order_key`, `ha_no_evacuate_key`,
  `ha_vm_state_reset_timeout`, `ha_fence_failed_interfaces[]`, `ha_fence_ceph`,
  `ha_fence_bmc`, `ha_fence_nova`, `ha_check_failed_interfaces[]`,
  `ha_bond_names[]`, `ha_power_fence_mode`, `ha_power_check_timeout`,
  `lb_no_migrate_key`, `executor_timeout`, `executor_max_attempts`,
  `executor_max_repeated_errors`, `executor_manual_action_timeout`.
- **HostAggregateConfigDTO**: `id`, `availability_zone_name`,
  `host_aggregate_name`, `marker` (`LB|HA|HA+LB`), `no_op_mode`,
  `no_op_mode_reason`, `ha_reservation_ratio_{cpu,disk,ram}`,
  `lb_{cpu,ram,network}_weight`, `lb_recommendations_auto_run`,
  `lb_threshold_{overload,limit}`, `lb_period`, `deleted`, `created_at`.
- **AZoneDTO**: `name`, `aggregates_count`, `aggregates_event_counts`
  (`active/warning/error/noop`), `aggregates_need_attention[]`.
- **HostAggregateEventDTO**: `id`, `region_data_id`, `host_aggregate_config_id`,
  `temporal_task_id`, `marker`, `status` (`created|generating|generated|
  wait_manually|executing|completed`), `state` (`healthy|warning|error`),
  `error_details`, `deleted`, `created_at`.
- **RecommendationDTO**: `id`, `host_aggregate_event_id`, `vm_uuid`, `source_hv`,
  `source_hv_name`, `destination_hv`, `destination_hv_name`,
  `source_hv_pre_load`, `destination_hv_pre_load`, `status`, `type`, `reason`,
  `error_details`, `evacuate_priority`, `created_at`.
- **OperationDTO**: `id`, `recommendation_id`, `status`, `openstack_request_id`,
  `nova_migration_id`, `error_details`, `failure_type`, `created_at`.

## Recommended implementation (best-practice choice)

**Do not port the SDK/CLI split as two Go modules. Implement KeyVRM as a native
koc service package** — one more first-class command group alongside baremetal,
dns, etc. Reasons, grounded in AGENTS.md:

- koc is a **single static, air-gapped binary**. A separate Go SDK module would
  fragment the vendored build, duplicate auth/TLS/output, and add a dependency
  surface. A native package reuses `internal/auth` (token, TLS, `--debug`,
  `--creds-from-vault`) and `internal/output` for free.
- KeyVRM is **already in the Keystone catalog**, so it's exactly the shape of
  every other koc service — resolve the endpoint from the catalog, issue
  authenticated requests. No custom auth like ironic's `--creds-from-ns`.
- There is no gophercloud `keyvrm` package, so requests use the **raw
  `ServiceClient.Get/Put/Post` pattern** koc already uses for untyped
  subresources (per the AGENTS.md "gophercloud gotchas" note) — decoding into
  koc-owned DTO structs.

Keep the **SDK/CLI separation logically, inside the package**: a typed request
layer (`requests.go`/`types.go`, returns DTOs, no cobra/output) under a thin
cobra+output CLI layer. That mirrors the Python split without a second module.

### Endpoint resolution (the one new mechanic)

```go
// internal/auth/services.go — new factory
func (c *Client) KeyVRM() (*gophercloud.ServiceClient, error) {
    if err := c.requireKeystone("keyvrm"); err != nil { // not available under --creds-from-ns
        return nil, err
    }
    if c.opts.KeyVRMEndpoint != "" {                     // --keyvrm-endpoint / KEYVRM_ENDPOINT_OVERRIDE
        return c.keyvrmClientAt(c.opts.KeyVRMEndpoint), nil
    }
    eo := c.Endpoint
    eo.Type = "keyvrm"
    eo.ApplyDefaults("keyvrm")
    url, err := c.Provider.EndpointLocator(eo)           // resolve from catalog
    if err != nil {
        return nil, wrapService("keyvrm", err)
    }
    return c.keyvrmClientAt(url), nil
}

func (c *Client) keyvrmClientAt(url string) *gophercloud.ServiceClient {
    if !strings.HasSuffix(url, "/") { url += "/" }
    return &gophercloud.ServiceClient{
        ProviderClient: c.Provider,
        Endpoint:       url,
        ResourceBase:   url + "v1/",   // paths are v1/<resource>
        Type:           "keyvrm",      // no OpenStack microversion header
    }
}
```

`X-Auth-Token` is emitted automatically by gophercloud from the provider token.

### Package layout (mirrors the koc command pattern)

```
internal/cli/keyvrm/
  keyvrm.go            NewCommand(a,o) → the `keyvrm` parent group
  client.go            newKeyVRMClient(ctx,a) → *gophercloud.ServiceClient (via auth.Client.KeyVRM)
  types.go             DTO structs + query-param builders (the "SDK" layer)
  requests.go          typed calls: getAppConfig / listHostAggregates / ... (raw ServiceClient)
  appconfig.go         app-config show/set
  hostaggregate.go     host-aggregate-config list/show/set/markers/noop/events
  availabilityzone.go  availability-zone list / host-aggregate list
  event.go             event show / recommendation list / recommendation run-all
  recommendation.go    recommendation list/show/run/stop + operation list
  *_test.go            httptest + runXxx seams (assert method/URL/query/body/output)
```

Each verb follows the AGENTS.md pattern: `RunE` → `o.Validate()` → build client via
`client.go` → delegate to a `runXxx(ctx, sc, o, args, w)` seam → route through
`o.WriteList`/`o.WriteSingle`. List verbs pass `limit`/`offset` straight through
as query params (KeyVRM paginates server-side) and surface `total` in a note.

### Command mapping (`kvrm` → `koc`, OSC-aligned verbs)

Namespaced under a `keyvrm` parent group to avoid clashing with OpenStack nouns
(`az`, `event` are generic) and to signal the embedded service.

| `kvrm` | proposed `koc` |
|---|---|
| `app-config get` | `koc keyvrm app-config show` |
| `app-config update …` | `koc keyvrm app-config set …` |
| `ha-config list` | `koc keyvrm host-aggregate-config list` |
| `ha-config get <id>` | `koc keyvrm host-aggregate-config show <id>` |
| `ha-config update <id>` | `koc keyvrm host-aggregate-config set <id>` |
| `ha-config markers` | `koc keyvrm host-aggregate-config markers` |
| `ha-config noop-on/off <id>` | `koc keyvrm host-aggregate-config set <id> --no-op-mode true/false [--reason]` |
| `ha-config events <id>` | `koc keyvrm host-aggregate-config event list <id>` |
| `az list` | `koc keyvrm availability-zone list` |
| `az host-aggregates <az>` | `koc keyvrm availability-zone host-aggregate list <az>` |
| `event get <id>` | `koc keyvrm event show <id>` |
| `event recommendations <id>` | `koc keyvrm event recommendation list <id>` |
| `event run-all <id>` | `koc keyvrm event recommendation run <id>` (all) |
| `recommendation list` | `koc keyvrm recommendation list` |
| `recommendation get <id>` | `koc keyvrm recommendation show <id>` |
| `recommendation operations <id>` | `koc keyvrm recommendation operation list <id>` |
| `recommendation run/stop <id>` | `koc keyvrm recommendation run/stop <id>` |

Two-word nouns (`host-aggregate-config event`, `availability-zone host-aggregate`)
are modeled as nested cobra commands, per the koc convention for OSC two-word
nouns.

### Wiring

- `internal/auth/options.go`: add `KeyVRMEndpoint` + `--keyvrm-endpoint`
  (env `KEYVRM_ENDPOINT_OVERRIDE`) and `--keyvrm-interface`/`--keyvrm-region`
  only if needed (else reuse `--os-interface`/`--os-region-name`).
- `internal/auth/services.go`: `KeyVRM()` factory (above).
- `internal/cli/root.go`: `root.AddCommand(keyvrm.NewCommand(authOpts, outOpts))`.

### Testing (per AGENTS.md)

httptest + the `runXxx` seam: build a fake `*gophercloud.ServiceClient`
(`Type:"keyvrm"`, `ResourceBase = ".../v1/"`) pointed at the mock; assert request
method, URL (`v1/host_aggregates/{id}` etc.), query params, PUT body
(`exclude_none` → only set fields), and rendered table/json. Cover each list plus
one write/trigger per resource.

## Size estimate

Moderate: ~5 command files + `types.go`/`requests.go` + the factory and one
flag. No new dependencies (raw gophercloud `ServiceClient` + stdlib json), so the
air-gap build is unaffected. Comparable to adding the `dns` service. Enums map to
Go string constants; validation mirrors the Python `Choice`/pydantic enums.

## AGENTS.md updates when it lands

- Layout: add `internal/cli/keyvrm/` and note it's the first **non-standard
  catalog service** (endpoint resolved by type `keyvrm`, no gophercloud package).
- Services list: add `keyvrm` (Keystack Virtual Resource Manager).

## Decisions (defaults chosen; override before coding if you disagree)

1. **Command namespace** — DECIDED: `koc keyvrm <noun> <verb>` (namespaced under a
   `keyvrm` parent). Avoids clashes with generic OpenStack nouns and signals the
   embedded service.
2. **Verb style** — DECIDED: OSC-aligned `show/set/list/run/stop`, for consistency
   with the rest of koc (the `kvrm` names are kept in the mapping table for
   discoverability).
3. **`--limit` semantics** — DECIDED: pass through to KeyVRM's server-side
   `limit`/`offset` (it paginates natively); surface `total` as a note.

Still open:

4. **Endpoint override name**: `--keyvrm-endpoint` (+ env `KEYVRM_ENDPOINT_OVERRIDE`
   for parity with the Python CLI) — confirm the env name.
5. **App-config `set`** exposes ~20 fields — mirror the full `kvrm` flag set, or
   start with the common subset and add the rest on demand?
