# Cloud verification: parity pass of 2026-08-07

Prompt for the follow-up session that has access to a real KeyStack cloud. It
verifies the 13 fixes on branch `claude/baremetal-abort-wait-parity-x8nraa`
(base `f34480d`, head `8792f1d`).

Everything below was reproduced with `httptest` + gophercloud fixtures and is
green in `go test ./...`. What unit tests **cannot** prove is the shape of the
real API — three of these bugs existed precisely because a fixture encoded a
payload designate/ironic never sends. **The point of this run is to confirm the
live payloads, not to re-run the unit tests.**

---

## How to use this document

You are verifying, not developing. For each check:

1. Run the command against the real cloud.
2. Compare against **Expected**.
3. Record PASS / FAIL / BLOCKED with the actual output pasted in.

Rules:

- Add `--debug` whenever the check is about a request (header, method, body) and
  paste the relevant request/response lines. `--debug` redacts tokens.
- **Do not "fix" a check by changing the test.** If reality disagrees with
  Expected, reality wins: record the real payload verbatim, and say which fix
  needs revisiting.
- Several checks are **destructive** (quota writes, abort, port security). They
  are marked. Run them only against a project you own and can restore; each one
  states how to restore it.
- Capture the working baseline first (§0) so you can put everything back.
- If `koc` and `openstack` disagree, run the same operation with
  `python-openstackclient` and paste both. Upstream is the reference for
  behaviour; KeyStack is the reference for the API.

Report at the end: a PASS/FAIL table, every real payload that differed from the
fixtures, and anything that should be filed as a new finding.

---

## 0. Baseline (do this first)

```sh
koc --version
openstack --version

# Values you will need to restore afterwards.
koc loadbalancer quota show                 > /tmp/lb-quota-before.txt
koc dns quota list                          > /tmp/dns-quota-before.txt
koc port show <PORT> -f json                > /tmp/port-before.json
```

Pick and note: a baremetal node that can be inspected, a DNS zone with an
export, a load balancer project, a port you can toggle security on, and a
**second** project ID you have admin rights over (for the dns-quota checks).

---

## 1. `baremetal node abort --wait` (P0, commit `4d472c2`)

The bug: after a successful abort ironic leaves `provision_state="inspect
failed"` **with `target_provision_state` still populated**. `--wait` only
returned when the target cleared, so it spun to timeout and exited 1 on success.

**Destructive**: aborts an in-flight inspection. Restore by re-running
`koc baremetal node manage <node>` / `inspect <node>`.

```sh
koc baremetal node inspect <NODE>
# while it is inspecting, in another shell:
time koc baremetal node abort <NODE> --wait --wait-timeout 5m ; echo "exit=$?"
```

**Expected**

- Exits **0**, in seconds, not at the timeout.
- stdout: `Node <id> settled in provision state "inspect failed"` followed by
  `Last error: Inspection was aborted by request.`

**Also capture — this is the payload the fixture got wrong:**

```sh
koc baremetal node show <NODE> -f json | \
  jq '{provision_state, target_provision_state, last_error}'
```

Paste it. Confirm `target_provision_state` is **non-empty** (`"manageable"`)
while `provision_state` is `"inspect failed"`. If it is empty, the premise of
the fix is wrong for this ironic version — say so.

Second path — an abort that ends with the target cleared (abort a deploy, which
lands on `available`/`deploy failed`): confirm `--wait` also exits 0 there.

Timeout message check (no restore needed):

```sh
koc baremetal node deploy <NODE> --wait --wait-timeout 10s ; echo "exit=$?"
```
Expected: exits non-zero with `(last provision_state "deploying")` — or whatever
state it was really in — inside the message, not a bare deadline error.

---

## 2. `dns service list/show` (P0, commit `17cf56f`)

The bug: `stats` and `capabilities` were decoded as `[]string`, but designate
sends **objects**. Every call failed with `json: cannot unmarshal object into Go
struct field ... of type []string`.

```sh
koc dns service list
koc dns service list -f json
koc dns service show <ID-from-the-list>
```

**Expected**: all succeed. Empty stats/capabilities render `-`; a populated one
renders `key=value`, one per line, sorted by key.

**Capture the raw payload** — this is what the fixture had wrong:

```sh
koc --debug dns service list 2>&1 | grep -A5 service_statuses
```

Paste the real `stats`/`capabilities` values. If any deployment sends an
**array** rather than an object, the type must accept both — file it.

---

## 3. `loadbalancer quota unset` / `reset` (P0, commit `4079559`, **BREAKING**)

The bug: `unset` took no flags and cleared all seven quotas. That is upstream's
`reset`.

**Destructive.** Restore from `/tmp/lb-quota-before.txt` with
`koc loadbalancer quota set <PROJECT> --loadbalancer N --listener N …`.

```sh
koc loadbalancer quota set <PROJECT> --loadbalancer 5 --listener 5 --pool 4
koc loadbalancer quota show <PROJECT>          # confirm 5 / 5 / 4

koc --debug loadbalancer quota unset <PROJECT> --listener
koc loadbalancer quota show <PROJECT>
```

**Expected**

- The `--debug` request is `PUT /v2.0/quotas/<PROJECT>` with body
  `{"quota": {"listener": null}}` — **not** a DELETE, and no other key present.
- `listener` reverts to the default; **`loadbalancer` is still 5 and `pool`
  still 4.** This is the whole bug — check it carefully.

```sh
koc loadbalancer quota unset <PROJECT>          # no flags
```
Expected: fails with "nothing to unset", changing nothing.

```sh
koc --debug loadbalancer quota reset <PROJECT>
```
Expected: `DELETE /v2.0/quotas/<PROJECT>`; all seven revert.

**Confirm the null-PUT is actually honoured by this octavia** — if it 400s, or
if it is accepted but does not reset the key, the raw fallback needs rethinking.
Compare with `openstack loadbalancer quota unset <PROJECT> --listener`.

---

## 4. Multi-line output (P1, commit `0886fe9`)

The bug: table and `-f value` collapsed embedded newlines to spaces, so
`zone export showfile` produced an unusable zonefile.

**The full documented round trip — this is the check that matters:**

```sh
koc zone export create <ZONE>
koc zone export list
koc zone export showfile <EXPORT-ID> -f value > /tmp/zone.txt

head -5 /tmp/zone.txt          # must be a real, multi-line zonefile
wc -l /tmp/zone.txt            # must be > 1
koc zone import create /tmp/zone.txt   # must be accepted
koc zone import list
```

**Expected**: `/tmp/zone.txt` is byte-identical to what designate served, and
the import is accepted. If the import fails, paste designate's error.

Also check:

```sh
koc dns pool list              # NS records on separate lines within the row
koc catalog list               # endpoints on separate lines within the row
koc dns service list -f csv    # multi-line cells RFC-4180 quoted
koc dns pool list -f json      # unchanged (was already correct)
```

Confirm the table borders still line up with multi-line cells, and that
`koc dns pool list | cat` (piped, unbounded width) is not mangled.

---

## 5. Unknown verbs exit non-zero (P2, commit `c7ef61a`)

```sh
for c in "zone bogusverb" "catalog bogusverb" "server bogusverb" \
         "loadbalancer quota bogusverb" "bogusnoun"; do
  koc $c >/dev/null 2>&1; echo "koc $c -> $?"
done
```
**Expected**: every one exits **1** (upstream cliff exits 2; koc uses its single
non-zero exit — note it if that matters to you).

```sh
koc zone ; echo "exit=$?"       # bare group: help, exit 0
koc zone --help                 # Usage block shows only "koc zone [command]"
```

Then a smoke pass that nothing became unreachable — every group must still list
its subcommands and every leaf must still run:

```sh
koc zone list && koc server list && koc baremetal node list && \
  koc loadbalancer list && koc network list && koc volume list
```

---

## 6. Command abbreviation (P2, commit `ed5ae00`)

```sh
koc server li --lim 1          # the reported failure
koc zon li
koc network trunk subp li
koc --debug server li --lim 1 2>&1 | head -3
```
**Expected**: all resolve and run. Compare `openstack server li --limit 1`.

```sh
koc zone sh ; echo "exit=$?"           # ambiguous (share/show) -> error
koc ser li ; echo "exit=$?"            # ambiguous -> error
koc --os-cloud <CLOUD> server li       # a flag VALUE must not be rewritten
```

Sanity: confirm `--os-cloud <CLOUD>` still authenticates against the right cloud
when the value happens to prefix a command name.

---

## 7. New commands (P2, commit `efa292d`)

```sh
koc catalog show keystone         # by name
koc catalog show identity         # by type
koc catalog show nosuchservice ; echo "exit=$?"    # non-zero
openstack catalog show keystone   # compare the field set
```

```sh
koc loadbalancer quota list
koc loadbalancer quota list --project <PROJECT>
koc --debug loadbalancer quota list 2>&1 | grep -i 'GET /v2'
openstack loadbalancer quota list
```

**Expected**: `GET /v2.0/quotas`. **Capture the raw payload** and confirm:

- the key spelling — `loadbalancer`/`healthmonitor` or the legacy
  `load_balancer`/`health_monitor` (both are handled; note which this cloud
  uses);
- whether `quotas_links` pagination actually appears — if it never does on a
  cloud with many projects, say so.

Compare the row set with upstream's; if upstream shows fewer columns, note it.

---

## 8. `dns quota` across projects (P2, commit `fc12b63`)

The bug: no cross-project header, so designate 403'd on GET and PATCH against
any project but your own.

**Destructive** on the second project. Restore with
`koc dns quota reset <OTHER-PROJECT>` or by re-setting the captured values.

```sh
koc --debug dns quota list <OTHER-PROJECT-ID> 2>&1 | grep -iE 'x-auth|GET /v2'
```
**Expected**: exits 0, and the request carries `X-Auth-All-Projects: true`
(set automatically because the target differs from the session project).

```sh
koc --debug dns quota list                       # OWN project
```
**Expected**: succeeds and sends **no** `X-Auth-*` header.

```sh
koc --debug dns quota set <OTHER-PROJECT-ID> --zones 25 2>&1 | grep -iE 'x-auth|PATCH'
koc dns quota list <OTHER-PROJECT-ID>            # shows zones=25
koc dns quota reset <OTHER-PROJECT-ID>
```

Flag surface:

```sh
koc dns quota list --project-id <OTHER-PROJECT-ID>
koc dns quota list <OTHER-PROJECT-ID> --sudo-project-id <OTHER-PROJECT-ID>
koc dns quota list <A> --project-id <B> ; echo "exit=$?"   # conflict -> non-zero
```

Confirm `--sudo-project-id` suppresses the automatic `--all-projects` (only the
sudo header should appear).

---

## 9. Port attributes (P2, commit `7375776`)

```sh
koc port show <PORT>
koc port show <PORT> -f json | jq '{allowed_address_pairs, port_security_enabled, fixed_ips}'
openstack port show <PORT> -f json | jq '{allowed_address_pairs, port_security_enabled, fixed_ips}'
```

**Expected**: both fields present; `fixed_ips` renders as
`ip_address='…', subnet_id='…'` per line rather than a JSON blob.

Round trip (**destructive**; restore from `/tmp/port-before.json`):

```sh
koc port set <PORT> --allowed-address ip-address=10.0.0.100
koc port show <PORT> -c allowed_address_pairs        # now visible
koc port set <PORT> --disable-port-security
koc port show <PORT> -c port_security_enabled        # False
koc port set <PORT> --enable-port-security
koc port unset <PORT> --allowed-address ip-address=10.0.0.100
```

Note: disabling port security may require detaching security groups first —
if neutron rejects it, that is the cloud's rule, not a koc bug.

On a deployment **without** the port-security extension, `port_security_enabled`
must render empty / `null`, never `false`. If you have such a cloud, check it.

---

## 10. Keystone ID passthrough (P3, commit `f982aff`)

The bug: only dashed UUIDs were passed through, so every 32-char undashed
Keystone ID caused a doomed `GET /v3/projects?name=<id>` first.

```sh
koc --debug quota show --project <32-CHAR-UNDASHED-PROJECT-ID> 2>&1 | grep 'projects?name='
```
**Expected**: **no output** — no name lookup is issued.

```sh
koc --debug quota show --project <PROJECT-NAME> 2>&1 | grep 'projects?name='
```
**Expected**: the lookup IS issued (names must still resolve).

---

## 11. `--timing` (P3, commit `e40077d`)

```sh
koc --timing server list -f json > /tmp/out.json 2> /tmp/timing.txt
jq . /tmp/out.json >/dev/null && echo "stdout is clean JSON"
tail -2 /tmp/timing.txt
```
**Expected**: stdout is valid JSON with no timing lines; stderr ends with
`timing: total N request(s) in D`.

```sh
koc --timing bogusnoun 2>&1 | tail -2        # total still printed on failure
koc server list 2>&1 | grep -c timing:       # 0 without --timing
```

This is a **documented deviation** (upstream writes a table to stdout). Confirm
the stderr choice is acceptable to you; if you want upstream's table, say so and
it will be changed.

---

## 12. DNS timestamps (P3, commit `af32b7a`)

```sh
koc zone show <ZONE> -f json | jq '{created_at, updated_at, transferred_at}'
koc zone export list -f json | jq '.[0] | {created_at, updated_at}'
koc recordset list <ZONE> -f json | jq '.[0]'
koc tsigkey list -f json
```

**Expected**: one format everywhere — `2026-08-07T12:00:00.000000` — and `null`
(empty in table output) for an absent value.

```sh
koc zone show <NEVER-TRANSFERRED-ZONE> -f value | grep 0001-01-01
```
**Expected**: **no match.** Go's zero time must appear nowhere.

Confirm designate's own timestamp precision matches the rendered format on this
cloud; if it sends a timezone offset or a different fraction width, paste it.

---

## 13. `--wait` failure output (P3, commit `7b32d22`)

**Destructive**: creates a load balancer. Delete it afterwards.

```sh
koc loadbalancer create --name verify-wait --vip-subnet-id <SUBNET> \
    --wait --wait-timeout 5s ; echo "exit=$?"
```

**Expected**: exits non-zero, **and stdout carries the full load balancer
record including its ID** (previously stdout was empty and the ID existed only
inside the error text). The error names the last status, e.g.
`(last provisioning_status "PENDING_CREATE")`.

```sh
koc loadbalancer list | grep verify-wait     # it exists — that was the point
koc loadbalancer delete verify-wait --cascade --wait
```

Happy path — confirm exactly **one** record is emitted, not two:

```sh
koc loadbalancer create --name verify-ok --vip-subnet-id <SUBNET> \
    --wait -f json | jq -s 'length'    # must be 1
koc loadbalancer delete verify-ok --cascade --wait
```

---

## 14. Regression sweep

Nothing above should have broken the rest of the CLI.

```sh
for c in "server list" "server show <SERVER>" "flavor list" "image list" \
         "volume list" "network list" "subnet list" "port list" \
         "router list" "zone list" "recordset list <ZONE>" \
         "baremetal node list" "loadbalancer list" "project list" \
         "user list" "endpoint list" "catalog list" "quota show"; do
  koc $c >/dev/null 2>&1 || echo "FAILED: koc $c"
done
```

Then spot-check every output format on one list and one show:

```sh
for f in table json yaml value csv; do koc server list -f $f | head -3; done
```

`port list` and `dns pool list` changed their rendering — eyeball those two
against `openstack` and confirm the change is an improvement, not a surprise for
existing scripts.

---

## Report template

| # | Check | Result | Notes |
|---|-------|--------|-------|
| 1 | baremetal abort --wait | | |
| 2 | dns service list/show | | |
| 3 | lb quota unset/reset | | |
| 4 | multi-line / zonefile round trip | | |
| 5 | unknown verb exit code | | |
| 6 | command abbreviation | | |
| 7 | catalog show, lb quota list | | |
| 8 | dns quota cross-project | | |
| 9 | port attributes | | |
| 10 | Keystone ID passthrough | | |
| 11 | --timing | | |
| 12 | dns timestamps | | |
| 13 | --wait failure output | | |
| 14 | regression sweep | | |

Plus:

1. **Payloads that differed from the fixtures** — verbatim, with the command
   that produced them. These are the highest-value output of the run.
2. **New findings**, in the same form as the input report: observed behaviour,
   upstream reference, acceptance criterion.
3. **Anything left BLOCKED** and why (missing rights, no such resource, a
   server-side failure). The known server-side issues below are not koc bugs and
   are out of scope:
   - `zone share delete` returning "unexpected EOF" while succeeding (designate
     HTTP/2 bug; `curl` fails the same way, clean over HTTP/1.1);
   - `DELETE /v2/quotas/<other>` succeeding without `X-Auth-All-Projects` while
     GET/PATCH 403;
   - `baremetal driver list` 500 (needs a system-scoped token);
     ironic-inspector 503;
   - Octavia amphora provisioning failures;
   - `-c` column ordering following the resource's order, not the request's —
     upstream cliff does the same.
