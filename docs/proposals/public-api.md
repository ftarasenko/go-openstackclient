# Public Go API under `pkg/`

Status: phases 0–2 landed (scaffolding and guards; session, paging, resolve;
the keyvrm and image pilots). The remaining services move one
`refactor(<service>)` series at a time, following the checklist below.

## Why

Downstream importers need koc's OpenStack service layer — resolve-by-name,
paging, cross-service lookups, per-service operations, the KeyVRM client — for
multi-region resource views. Everything lived under `internal/`, which Go
forbids other modules to import. `pkg/` is that layer, public, documented and
versioned; the CLI is its first consumer, and its command surface, output and
release pipeline do not change.

## The packages as built

| Package | Exported surface | `go doc -all` lines |
| --- | --- | ---: |
| `pkg/openstack` | `Session` (`Provider`, `EndpointOpts`, `Region`, `Options`, `ForRegion`, `ScopedProjectID`, and the constructors `Baremetal`, `Introspection`, `Compute`, `Identity`, `Volume`, `DNS`, `Image`, `Network`, `Placement`, `LoadBalancer`, `KeyVRM`); `New(ctx, Config)`, `FromProvider(pc, eo, Options)`; `Config`, `Options`, `Microversions`, `DefaultMicroversion`, `KeyVRMServiceType`; `ServiceError`, `ErrNotFound` | 241 |
| `pkg/openstack/paging` | `Collect[T]` | 22 |
| `pkg/openstack/resolve` | `ImageID`, `NetworkID`, `SubnetID`, `PortID`, `VolumeID`, `ServerID`, `ProjectID`, `ProjectIDInDomain`, `ProjectIDInDomainID`, `DomainID`, `UserID`, `UserIDInDomain`, `UserIDInDomainID` (package funcs and `Resolver` methods); `Resolver`, `Cache`, `NewCache`, `WithClock`, `CacheOption`; `NotFoundError`, `AmbiguousError`; `IsUUID` | 182 |
| `pkg/openstack/image` | `ListImages`, `FindByName`, `GetImage`, `DeleteImage`, `UpdateImage`, `EscapePropertyName`, `ActivateImage`, `DeactivateImage`, `CreateImage`, `CreateImageWithData`, `UploadImageData`, `StageImageData`, `DiscardImage`, `DownloadImage`, `CreateImageFromVolume`, `ImportImage`, `ImportOpts`, `GetImportInfo`, `AddMember`, `RemoveMember`, `ListMembers`, `UpdateMember`, `ListTasks`, `TaskListOpts`, `GetTask`, `ListStores`, `Store`, `WaitForStatus`, `Data`, `VisibilityAll`, `ErrGlanceDirectUnavailable` | 217 |
| `pkg/keyvrm` | `GetAppConfig`, `UpdateAppConfig`, `ListHostAggregates`, `GetHostAggregate`, `UpdateHostAggregate`, `GetMarkers`, `ListHostAggregateEvents`, `ListAvailabilityZones`, `ListZoneHostAggregates`, `GetEvent`, `ListEventRecommendations`, `RunEventRecommendations`, `ListRecommendations`, `GetRecommendation`, `ListRecommendationOperations`, `RunRecommendation`, `StopRecommendation`; the DTOs; `Page[T]`, `ListOpts` | 186 |

Stays internal, CLI only: `internal/cli/**` (cobra commands, flag tables,
aliases, prefix expansion, `watch.go`), `internal/output`, `internal/watch`,
`internal/auth`'s flag/env/osc-compat precedence, the debug and timing
transports and `--creds-from-*`, `internal/kube`, `internal/vault` and
`vaulttest`, `internal/s3`, and the small CLI helpers (`allprojects`,
`batchdelete`, `extract`, `nameflag`).

## Decisions taken in the pilots

**Session.** `pkg/openstack.Session` is immutable: a ProviderClient, endpoint
options and `Options` (per-service microversions, a KeyVRM endpoint override).
`ForRegion` copies it with another region and the same ProviderClient, so a
multi-region view holds one token. `New` takes a plain `Config`: explicit
`gophercloud.AuthOptions`, or a cloud name plus clouds.yaml *bytes* (the caller
finds and reads the file), region, interface, and either a `*tls.Config` or an
`*http.Client` — the latter is how the CLI injects its debug and timing
transports. It reads no environment variable, with one documented exception
inside gophercloud's clouds.yaml parser (the `OS_CACERT`/`OS_CERT`/`OS_KEY`
fallback for an entry that names no TLS files).

`internal/auth` authenticates through `openstack.New`, handing it the
`AuthOptions` it resolved from flags, OS_* and clouds.yaml (all of koc's
precedence rules — auth types, endpoint overrides, the secure.yaml search,
osc-compat domain handling — stay there). `auth.Client` keeps its exported
`Provider`/`Endpoint` fields, which command-package tests construct literally,
and derives its Session on demand (`Client.Session()`); its service factories
wrap the Session's constructors. The microversions are the one split: the
public constructors default to `latest`, and the CLI factories then set the
`--os-*-api-version` value themselves, because a bad flag value is an error
that names the flag, and only for the service a command uses.
`auth.ServiceError` is an alias of `openstack.ServiceError`.

**Paging** moved as is, and its 19 call sites changed import path only — no
forwarder, since a one-line forwarder is a layer with nothing in it.

**Resolve.** The public resolvers are strict: no match is a
`*resolve.NotFoundError` (matching `openstack.ErrNotFound`), several are a
`*resolve.AmbiguousError`, with the CLI's message texts. A `Resolver` memoizes
single matches in a caller-owned `Cache` with a TTL, keyed by the client's
endpoint URL and the token's project as well as kind, scope and name — so one
Cache serves several regions and credentials safely (test:
`TestCache_KeepsRegionsApart`). `internal/cli/resolve` stays as the CLI's
policy: the literal-ref fallback on zero matches (applied to each lookup
separately, so `--project-domain` keeps its old behaviour) and the
process-wide memo `--watch` enables, now a `resolve.Cache`. Its tests pass
unedited.

**Errors.** Public functions wrap with the CLI's text and the ID they were
given. Three image verbs (`delete`, `import`, `stage`) have always named the
operator's reference instead; the CLI re-labels the same inner error
(`relabel`), so output is byte-identical. A message that names a CLI flag
(`--import: the cloud does not offer the glance-direct import method`) became
a sentinel (`image.ErrGlanceDirectUnavailable`) that the CLI prefixes.

**Shapes worth copying.** `image.CreateImageWithData` (a multi-step flow with
its cleanup, behind one call, with the single steps public too);
`image.ImportOpts` and `image.TaskListOpts` (gophercloud's opts plus the field
it misses, as a builder); `image.WaitForStatus` (context for the deadline,
explicit interval, terminal states fail fast, a deadline mid-read reports as
the wait timing out); `keyvrm.Page[T]`; request options built per call rather
than shared package vars.

## Rules

The rules live in AGENTS.md → "Public Go API" and are enforced by
`.golangci.yml` (depguard rule `public-api`; forbidigo scoped to `pkg/`),
`TestNoPackageState`, and two `supply-chain.yml` jobs: `importer` (the nested
module `test/importer`, which also fails if an importer's graph picks up cobra,
pflag, x/term or `internal/`) and `api-compat` (`scripts/api-compat.sh`,
apidiff between the latest `v*` tag and HEAD; reports only until a tag carries
`pkg/`).

## Per-service checklist

For each service, one `refactor(<service>)` series that moves only that
service:

1. Before the first change: build the binary and dump `koc <every leaf>
   --help` (walk "Available Commands") outside the repository; record `make
   size`.
2. For each `runXxx` seam, move its API half — opts building, the call,
   `AllPages`/`paging.Collect`, `Extract`, the error wrap — into
   `pkg/openstack/<service>` as a plain function on a
   `*gophercloud.ServiceClient`, returning gophercloud types. Keep the wrap text
   verbatim; where the CLI names a reference rather than an ID, re-label in the
   CLI; where it names a flag, export a sentinel.
3. Export koc's request extensions (raw-request fallbacks, body splices,
   query fixes) with doc comments saying what gophercloud lacks. Keep a
   microversion pin (e.g. nova 2.43 for the floating-IP actions) inside the
   public function, on a shallow copy of the client, as today.
4. Turn wait/poll loops into `WaitFor…(ctx, client, id, …, interval)` in the
   `image.WaitForStatus` shape. The CLI keeps its `--wait` flags, timeouts and
   progress output.
5. Typed errors importers need become exported types or sentinels in
   `pkg/openstack` (or the service package when service-specific).
6. Leave name resolution with a lenient fallback in the CLI; the public side
   uses `pkg/openstack/resolve` or the service's own strict lookups.
7. The seam keeps its signature; its body becomes the public call plus the
   existing render. Seam tests pass without edits (import paths aside); tests
   of the moved request layer move with it as black-box tests.
8. Add black-box tests for every public function (method, URL, microversion
   header, body) and an `Example…` for the service's common path.
9. Gate: gofmt (Go 1.27), vet, golangci-lint at 0, `go test ./...`, the
   functional package offline (`TestEveryLeafIsCovered`), the offline static
   build, `make crossbuild`, the privacy scrub. Then diff the `--help` dump
   (must be empty), run the moved verbs against a fake endpoint with the old
   and the new binary and diff their output, and compare `make size`.
10. `docs/coverage.md` does not change: no leaf is added, renamed or removed.

## Remaining services: size and effort

Measured on this branch. "Seams" counts `runXxx` functions; "raw" counts raw
`ServiceClient` calls (fallbacks that need exporting with care). A session is
one working session of the scope of phases 0–2 here; the image pilot (18
seams, 2k lines) took about a third of one.

| Order | Service (package) | src / test lines | Seams | Raw | Wait | Effort | Notes |
| ---: | --- | ---: | ---: | ---: | ---: | --- | --- |
| 1 | `compute`: flavors, keypairs (`internal/cli/compute`) | 1 215 / 1 845 | 10 | 5 | — | S, ½ session | Opens `pkg/openstack/compute`, following the API rather than the CLI tree. |
| 2 | `compute`: servers (`internal/cli/server`) | 10 256 / 11 132 | 63 | 37 | 2 | L, 3 sessions | `serverActionRaw` microversion pin, `readUserData` encoding, `serverRebuildOptsExt`, migration/live-migration waits, console POSTs; cross-service image/network/volume resolution takes the extra clients explicitly. |
| 3 | `volume` | 5 003 / 5 316 | 53 | 37 | 1 | M–L, 2 sessions | `wait.go`; raw cinder service enable/disable and other untyped actions; already strict resolution (`no %s found matching %q`). |
| 4 | `quota` + `crossservice` | 1 702 / 1 278 | 8 | 10 | — | S, ½–1 session | The cross-service calls (nova + cinder + neutron) take a `*openstack.Session`. |
| 5 | `network` core: floating IPs, ports, security groups (+ networks, subnets, routers, `helpers.go`) | 6 409 of 20 621 | 37 | 19 | — | M, 2 sessions | The strict helpers in `helpers.go` (`no %s found for %q`) become the public lookups. |
| 6 | `network`: the long tail (QoS, FWaaS, VPNaaS, BGP VPN, trunks, tap, …) | 14 212 | 166 | 69 | — | L, 4 sessions | Extension-gated; keep each extension's file as its own public file. |
| 7 | `loadbalancer` | 5 367 / 3 008 | 62 | 28 | 1 | M, 2 sessions | `wait.go` provisioning poll. |
| 8 | `dns` | 4 945 / 3 924 | 60 | 18 | — | M, 1½ sessions | Lenient resolvers move to strict on the public side. |
| 9 | `identity` | 3 784 / 3 116 | 57 | 21 | — | M, 1½ sessions | Lenient resolvers; application credentials, roles. |
| 10 | `placement` | 2 076 / 2 094 | 31 | 13 | — | S–M, 1 session | Microversion-gated calls. |
| 11 | `baremetal` (+ inspector) | 8 148 / 5 532 | 99 | 20 | 2 | L, 3 sessions | `explainMicroversion` → an exported `MicroversionError` (service, feature, required, cloud max) so importers can `errors.As`; provision-state `--wait`; both inspection dialects; `--creds-from-ns`'s standalone basic-auth ironic stays CLI-side (an importer brings its own ProviderClient). |

Total: about 20 sessions, network and servers being half of it.

## Constraints from neighbouring work

- The embedded terminal mode (`koc shell`, the embedded profile, `KOC_HOME`)
  landed on `master` alongside this work and needs nothing from `pkg/` but what
  the rules already require: no file paths, prompts or environment reads in
  `pkg/`, and the feature groups it turns off (Vault, S3, kube, consoles) all
  CLI-side.

## Decisions (owner, 2026-10-17)

1. **`kolladefaults` and `deployment` stay out of koc altogether** — not public
   packages later. Validating kolla defaults is LCM logic and moves to the LCM
   tooling, outside this repository; `feat/deployment-validation` stays parked
   and nothing from it lands here.
2. **`internal/vault`, `internal/s3` and `internal/kube` stay internal.**
   Importers use each system's official client.
3. **The module path stays** `github.com/ftarasenko/go-openstackclient`.
4. **Versioning:** the version keeps bumping by the existing rules; `v1.0.0`
   is not tied to a downstream release.
5. **Return gophercloud types**, as built. koc-owned types exist only for koc's
   own extensions (raw-request fallbacks, request-body splices, documents
   gophercloud does not model — `image.ImportOpts`, `image.Store`, the KeyVRM
   DTOs).
