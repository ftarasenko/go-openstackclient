# Public Go API under `pkg/`

Status: phases 0–2 landed (scaffolding and guards; session, paging, resolve;
the keyvrm and image pilots), and compute's flavors, keypairs, aggregates,
hypervisors, compute services and every server verb (actions, migrations,
the lifecycle, server groups, volume attachments, events and the password)
have moved, and so has all of identity. Of volume, the volumes, their
actions and waits, volume attachments, snapshots, backups, volume types and
QoS have moved; transfers, services, backend pools and clusters follow.
The remaining services move one `refactor(<service>)` series at a time,
following the checklist below.

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
| `pkg/openstack/compute` | `ListFlavors`, `GetFlavor`, `FindFlavor`, `CreateFlavor`, `DeleteFlavor`, `ListFlavorAccess`, `AddFlavorAccess`, `RemoveFlavorAccess`, `SetFlavorExtraSpecs`, `DeleteFlavorExtraSpec`, `ClearFlavorExtraSpecs`, `UpdateFlavorDescription`, `FlavorDescriptionMicroversion`; `ListKeypairs`, `GetKeypair`, `CreateKeypair`, `DeleteKeypair`; `ListAggregates`, `FindAggregateID`, `HostAggregates`, `GetAggregate`, `CreateAggregate`, `UpdateAggregate`, `SetAggregateMetadata`, `DeleteAggregate`, `AddAggregateHost`, `RemoveAggregateHost`; `ListHypervisors`, `ListHypervisorsWithUsage`, `FindHypervisor`, `HypervisorIDs`, `PlacementUsage`, `ProviderUsage`; `ListServices`, `ExtractServiceAdminState`, `ServiceAdminState`, `FindServiceID`, `UpdateService`, `ServiceUpdateOpts`, `DeleteService`; `StartServer`, `StopServer`, `PauseServer`, `UnpauseServer`, `SuspendServer`, `ResumeServer`, `LockServer`, `UnlockServer`, `RebootServer`, `ResizeServer`, `ConfirmResize`, `RevertResize`, `RebuildServer`, `RebuildOpts`, `RebuildUserDataMicroversion`, `ShelveServer`, `ShelveOffloadServer`, `UnshelveServer`, `UnshelveOpts`, `RescueServer`, `UnrescueServer`, `CreateServerImage`, `AttachVolume`, `VolumeAttachOpts`, `DetachVolume`, `AddFloatingIP`, `RemoveFloatingIP`, `FloatingIPActionMicroversion`, `AddSecurityGroup`, `RemoveSecurityGroup`, `AddServerGroup`, `RemoveServerGroup`, `ConsoleOutput`, `CreateRemoteConsole`, `ListInterfaces`, `AttachInterface`, `DetachInterface`, `InterfaceAttachOpts`, `InterfaceTagMicroversion`, `SupportsMicroversion`, `WaitForServerStatus`, `WaitForServerDeleted`, `ErrServerSoftDeleted`; `MigrateServer`, `MigrateHostMicroversion`, `LiveMigrateServer`, `LiveMigrateOpts`, `LiveMigrateBody`, `LiveMigrateAutoMicroversion`, `EvacuateServer`, `EvacuateOpts`, `EvacuateBody`, `EvacuateForceRemovedMicroversion`, `ServerAction`, `ConfirmMigration`, `WaitForMigration`, `WaitForServerToLeaveHost`, `MigrationFailedError`, `ListServersOnHost`; `ListMigrations`, `MigrationListOpts`, `Migration`, `ListServerMigrations`, `GetServerMigration`, `ServerMigration`, `AbortServerMigration`, `ForceCompleteServerMigration`; `ListServers`, `ListServerNames`, `ListServersRaw`, `ServerListOpts`, `GetServer`, `GetServerRaw`, `EncodeUserData`, `CreateServer`, `ServerCreateOptsExt`, `DeleteServer`, `ForceDeleteServer`, `UpdateServer`, `ServerUpdateOpts`, `SetServerAvailabilityZone`, `UpdateServerMetadata`, `ReplaceServerMetadata`, `DeleteServerMetadatum`, `ChangeAdminPassword`, `ClearServerPassword`, `ResetServerState`, `AddServerTag`, `DeleteServerTag`, `DeleteServerTags`; `ListServerGroups`, `GetServerGroup`, `CreateServerGroup`, `DeleteServerGroup`, `ServerGroupPolicyMicroversion`; `ListVolumeAttachments`, `VolumeAttachment`, `SetVolumeDeleteOnTermination`, `VolumeAttachmentUpdateMicroversion`; `ListInstanceActions`, `InstanceAction`, `InstanceActionListOpts`, `GetInstanceAction`, `ListInstanceActionEvents`, `InstanceActionEvent`; `GetServerPassword` | 876 |
| `pkg/openstack/identity` | `ListDomains`, `GetDomain`, `CreateDomain`, `UpdateDomain`, `DeleteDomain`; `ListProjects`, `GetProject`, `CreateProject`, `UpdateProject`, `DeleteProject`; `ListUsers`, `GetUser`, `CreateUser`, `UpdateUser`, `DeleteUser`, `ChangeUserPassword`; `ListGroups`, `ListUserGroups`, `GetGroup`, `CreateGroup`, `UpdateGroup`, `DeleteGroup`, `AddUserToGroup`, `RemoveUserFromGroup`, `IsUserInGroup`; `ListRegions`, `GetRegion`, `CreateRegion`, `UpdateRegion`, `DeleteRegion`; `ListServices`, `GetService`, `CreateService`, `UpdateService`, `DeleteService`; `ListEndpoints`, `GetEndpoint`, `CreateEndpoint`, `UpdateEndpoint`, `DeleteEndpoint`; `ListCatalog`, `IntrospectToken`, `TokenInfo`, `TokenUser`, `RevokeToken`; `ListRoles`, `GetRole`, `CreateRole`, `UpdateRole`, `DeleteRole`, `AssignRole`, `UnassignRole`, `ListRoleInferenceRules`, `CreateRoleInferenceRule`, `DeleteRoleInferenceRule`; `ListRoleAssignments`, `RoleAssignment`; `ListApplicationCredentials`, `GetApplicationCredential`, `CreateApplicationCredential`, `DeleteApplicationCredential` | 236 |
| `pkg/openstack/volume` | `ListVolumes`, `ListOpts`, `GetVolume`, `CreateVolume`, `UpdateVolume`, `MetadataUpdateOpts`, `DeleteVolume`; `ExtendVolume`, `ResetVolumeStatus`, `ResetStatusOpts`, `ErrNothingToReset`, `RetypeVolume`, `SetVolumeBootable`, `MigrateVolume`, `MigrateOpts`; `WaitForVolumeAvailable`, `WaitForRetype`, `WaitForVolumeMigration`, `State`, `WaitFailedError`, `WaitFailure` (`ErrorStatus`, `MigrationError`, `RetypeRolledBack`); `ListAttachments`, `GetAttachment`, `CreateAttachment`, `UpdateAttachment`, `DeleteAttachment`, `CompleteAttachment`, `AttachmentsMicroversion`, `CompleteAttachmentMicroversion`, `AttachModeMicroversion`; `ListSnapshots`, `GetSnapshot`, `CreateSnapshot`, `UpdateSnapshot`, `ReplaceSnapshotMetadata`, `ResetSnapshotStatus`, `DeleteSnapshot`; `ListBackups`, `GetBackup`, `CreateBackup`, `UpdateBackup`, `ResetBackupStatus`, `DeleteBackup`, `RestoreBackup`; `ListVolumeTypes`, `GetVolumeType`, `CreateVolumeType`, `UpdateVolumeType`, `DeleteVolumeType`, `SetVolumeTypeExtraSpecs`, `DeleteVolumeTypeExtraSpec`; `ListQoS`, `ListQoSAssociations`, `GetQoS`, `CreateQoS`, `DeleteQoS`, `SetQoSSpecs`, `DeleteQoSKeys`, `AssociateQoS`, `DisassociateQoS`, `DisassociateAllQoS` | 302 |
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

Compute's flavor verbs all name the operator's reference, so the public
functions keep the CLI's texts with the quoted flavor ID, and
`internal/cli/compute` swaps that ID for the reference (`relabel`). The
flavor lookup was already strict; `compute.FindFlavor` returns
`*resolve.NotFoundError`/`*resolve.AmbiguousError`, and the CLI keeps its
own wording for both.

The aggregate, hypervisor and compute service verbs in `internal/cli/server`
name the operator's host and binary rather than nova's IDs, so there the CLI
re-wraps the inner error with its own text (`relabel`, as image does). The
aggregate, hypervisor and service lookups return the same typed errors.
`compute.ServiceUpdateOpts` replaces gophercloud's `services.UpdateOpts`,
which cannot send `forced_down: false` and has no field for KeyStack's admin
state. `compute.ListHypervisorsWithUsage` reads at 2.1 because nova dropped the
usage fields at 2.88. `compute.PlacementUsage` takes the placement client and
returns what those fields became.

The server actions take nova's server ID and name it in their errors; the CLI
resolves the operator's reference first and re-labels, as above. Where the
CLI printed gophercloud's error bare (start, stop, pause and the other simple
actions, `server add network`), it unwraps the public error instead. The
floating-IP actions pin a copy of the client to 2.43, since nova removed them
at 2.44; `compute.RebuildOpts`, `compute.UnshelveOpts`,
`compute.VolumeAttachOpts` and `compute.InterfaceAttachOpts` add the fields
gophercloud's opts miss (rebuild user data, unshelve host, volume tag and
delete-on-termination, interface tag). `compute.WaitForServerStatus` and
`compute.WaitForServerDeleted` follow `image.WaitForStatus`; the CLI keeps
`--wait-timeout` and the poll interval, and adds `; use --force to delete it
now` to `compute.ErrServerSoftDeleted`. The server-group membership wait and
the snapshot's image wait stay in the CLI: both are worded around the
operator's reference.

The server lifecycle verbs keep their requests: `compute.ListServers`,
`compute.ListServerNames` (the non-detail listing) and `compute.ListServersRaw`
(the attributes `servers.Server` drops, for `server list -c`) all take the
`compute.ServerListOpts` filters, and the caller pins the microversion on a
client copy. `compute.ServerCreateOptsExt` carries 2.74's `host` and
`block_device_mapping_v2`; `compute.EncodeUserData` is the encoding
`servers.CreateOpts.UserData` needs before it guesses. Server lookup by name
lists through `compute.ListServerNames` but keeps the CLI's wording and
policy: `resolve.ServerID` resolves an empty reference to itself without a
request, where the CLI has always listed and failed. Flavor lookup in
`server create` and `server resize` keeps its single public listing for the
same reason; `compute.FindFlavor` would retry a miss across every access type.

The rest of `server` is plain calls. `compute.ListInstanceActions` sends
`limit` and caps the result, because nova treats it as a page size;
`compute.GetInstanceAction` returns the raw record, which `server event show`
renders as it came. `compute.VolumeAttachment` adds 2.89's `attachment_id` and
`bdm_uuid`, which gophercloud's type drops. `compute.GetServerPassword`
decrypts with the caller's key and returns the ciphertext when it gets none.
Server group lookup by name keeps the CLI's literal-reference fallback
(README "Known limitations"); moving it to the strict form is a behaviour
change, not part of the move.

Migration builds its bodies itself, because gophercloud's opts cannot express
them: `servers.LiveMigrateOpts` cannot send `"auto"` and drops
`block_migration`, and `servers.EvacuateOpts` always sends `onSharedStorage`.
`compute.LiveMigrateBody` and `compute.EvacuateBody` drop what the client's
microversion no longer has (`disk_over_commit` from 2.25, `force` from 2.68),
and the CLI warns about the drop. The host drains build the body once and post
it to each server through `compute.ServerAction`. The drain engine itself
(planning, the worker pool, the progress display) stays in the CLI. Its API
half is `compute.ListServersOnHost`, `compute.WaitForServerToLeaveHost` and
`compute.ConfirmMigration`, which reports the confirm nova made on its own
under `resize_confirm_window` as a result rather than an error. The waits
return `*compute.MigrationFailedError` for ERROR, and the CLI rewords it with
the operator's reference. The two pure classification tests moved to `pkg/`
with the logic they test. The os-migrations and os-server-migrations calls
are raw requests decoding into `compute.Migration` and
`compute.ServerMigration`, which gophercloud has no package for.

Identity is plain calls over gophercloud's keystone packages; every listing
collects all pages. `identity.IntrospectToken` returns the token with its
project and user (`identity.TokenInfo`), and `identity.TokenUser` reads only
the user. The CLI's name lookups list through the public functions but keep
their literal-reference fallback (README "Known limitations"): moving them to
the strict form, which `resolve.ProjectID`, `resolve.DomainID` and
`resolve.UserID` already offer, is a behaviour change, not part of the move.
`identity.ListRoleAssignments` returns `identity.RoleAssignment`, gophercloud's
assignment plus the two fields it drops (a domain role's domain and the
OS-INHERIT marker), read from the same page. Application credentials take the
owning user's ID; finding the current user from the auth result stays in the
CLI, since it reads the provider rather than calling the API.

Volume's waits follow compute's: context for the deadline, an explicit
interval, up to five failed reads in a row ridden out, and a
`*volume.WaitFailedError` (with the reason and the state last read) for a
volume that settled where the wait did not want it. The CLI rewords that and
the timeout with the operator's reference. `volume.ListOpts`,
`volume.MetadataUpdateOpts` and `volume.ResetStatusOpts` are the builders the
CLI already used for what gophercloud's opts get wrong (no `user_id` filter,
an empty metadata map dropped, an empty `status` always sent). The bootable
and migrate actions are raw POSTs, as they were. The CLI's GET-then-name
resolvers stay in the CLI and list through the public calls, except the
backup resolver, which keeps gophercloud's summary listing: `ListBackups`
reads the detail view and sends the same filters there. `UpdateBackup` adds
the `backup` wrapper gophercloud leaves off the body.

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
| 1 | `compute`: flavors, keypairs (`internal/cli/compute`) | 1 215 / 1 845 | 10 | 5 | — | **Done** | `pkg/openstack/compute`, laid out by API resource rather than the CLI tree. |
| 2 | `compute`: servers (`internal/cli/server`) | 10 256 / 11 132 | 63 | 37 | 2 | L, 3 sessions; **done** (aggregates, hypervisors, services, actions, migrations, the lifecycle, server groups, volume attachments, events, password) | `serverActionRaw` microversion pin, `readUserData` encoding, `serverRebuildOptsExt`, migration/live-migration waits, console POSTs; cross-service image/network/volume resolution takes the extra clients explicitly. |
| 3 | `volume` | 5 003 / 5 316 | 53 | 37 | 1 | M–L, 3 sessions; volumes, waits, attachments, snapshots, backups, types and QoS **done** | `wait.go`; raw cinder service enable/disable and other untyped actions; already strict resolution (`no %s found matching %q`). |
| 4 | `quota` + `crossservice` | 1 702 / 1 278 | 8 | 10 | — | S, ½–1 session | The cross-service calls (nova + cinder + neutron) take a `*openstack.Session`. |
| 5 | `network` core: floating IPs, ports, security groups (+ networks, subnets, routers, `helpers.go`) | 6 409 of 20 621 | 37 | 19 | — | M, 2 sessions | The strict helpers in `helpers.go` (`no %s found for %q`) become the public lookups. |
| 6 | `network`: the long tail (QoS, FWaaS, VPNaaS, BGP VPN, trunks, tap, …) | 14 212 | 166 | 69 | — | L, 4 sessions | Extension-gated; keep each extension's file as its own public file. |
| 7 | `loadbalancer` | 5 367 / 3 008 | 62 | 28 | 1 | M, 2 sessions | `wait.go` provisioning poll. |
| 8 | `dns` | 4 945 / 3 924 | 60 | 18 | — | M, 1½ sessions | Lenient resolvers move to strict on the public side. |
| 9 | `identity` | 3 784 / 3 116 | 57 | 21 | — | M, 1½ sessions; **done** | Lenient resolvers; application credentials, roles. |
| 10 | `placement` | 2 076 / 2 094 | 31 | 13 | — | S–M, 1 session | Microversion-gated calls. |
| 11 | `baremetal` (+ inspector) | 8 148 / 5 532 | 99 | 20 | 2 | L, 3 sessions | `explainMicroversion` → an exported `MicroversionError` (service, feature, required, cloud max) so importers can `errors.As`; provision-state `--wait`; both inspection dialects; `--creds-from-ns`'s standalone basic-auth ironic stays CLI-side (an importer brings its own ProviderClient). |

Total: about 20 sessions, network and servers being half of it.

The order now follows a downstream importer's needs rather than the "Order"
column: compute servers (aggregates, hypervisors and services first, then
actions, then migration and evacuate, then create), identity, volume, quota
and crossservice, network core, placement, then the rest as listed.

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
