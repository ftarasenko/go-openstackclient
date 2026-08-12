# Security policy

`koc` is an OpenStack client. It reads cloud credentials from `clouds.yaml`,
`OS_*` environment variables, HashiCorp Vault and Kubernetes secrets, holds a
Keystone token for the life of an invocation, and makes authenticated TLS calls
to cloud endpoints. Credential handling and certificate verification are the
parts of it worth attacking, so they are the parts this policy is about.

## Supported versions

**Only the latest release is supported.** There are no backports and no
maintenance branches: a fix ships in the next release, and the upgrade path for
every earlier version is to move to the newest one.

| Version | Supported |
| --- | --- |
| latest release | yes |
| anything older | no — upgrade |

While the project is `0.y.z` there is no stable API, so a minor release can
change behaviour. The release notes call out breaking changes under their own
heading; read them before upgrading a pinned deployment.

## Reporting a vulnerability

Use GitHub's **private vulnerability reporting** on this repository
(Security → Report a vulnerability). It creates a private thread visible only to
the maintainers.

**Do not open a public issue** for anything touching credentials, TLS
verification, `--debug` output or the release pipeline. A public report on those
is itself an exposure, because it tells everyone running an unpatched binary
where to look.

Please include the `koc` version (`koc --version`), what you observed, and how to
reproduce it. If a reproduction needs request/response captures, **redact them
first** — `--debug` output contains tokens, passwords and endpoint addresses.

Expect an acknowledgement within a few working days. This is a small project;
there is no 24/7 rotation, and an honest slow answer is better than a promised
fast one.

## In scope

- Credential handling: `--creds-from-vault`, `--creds-from-ns`, `clouds.yaml`
  and `OS_*` parsing, and anything that causes a credential to reach a host it
  was not intended for.
- TLS: certificate verification, the `--insecure` and `OS_INSECURE` paths, CA
  bundle and mTLS handling.
- `--debug` redaction — any secret that survives into debug output.
- Secrets reaching disk or terminal output where they should not, including
  `koc vault kv export`.
- The release supply chain: the build, the SBOMs, the signature, the
  provenance attestation.

## Out of scope

- A misconfigured or vulnerable OpenStack cloud. `koc` is a client; report those
  upstream.
- Anything requiring an attacker who already controls the machine running `koc`
  or the credentials it was given.
- Missing hardening with no exploitable consequence. It may still be a welcome
  issue — just not a vulnerability report.

## Verifying a release

Every release ships a `checksums.txt` covering all artifacts, a **keyless cosign
signature** over that file, and an **SPDX SBOM** per artifact. `checksums.txt`
on its own proves nothing — whoever could swap an artifact could swap the
checksums with it — so the signature is what roots the trust, and the checksums
chain from it to each artifact.

Verify the signature, then the artifact:

```sh
cosign verify-blob checksums.txt \
  --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/ftarasenko/go-openstackclient/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

sha256sum -c checksums.txt
```

No key distribution is involved: the signing identity is the release workflow
itself, asserted by the certificate. The `--certificate-identity-regexp` and
`--certificate-oidc-issuer` flags are the check that matters — without them you
have verified only that *somebody* signed the file.

Provenance — what built the artifact, from which commit and workflow:

```sh
gh attestation verify koc_<version>_linux_amd64.tar.gz \
  --repo ftarasenko/go-openstackclient
```

The SBOMs are `<artifact>.spdx.json`, SPDX 2.3 JSON as produced by syft, for
feeding an inventory or licence scanner.

### Verifying without internet access

The commands above are the reason the air-gap matters here: by default
`cosign verify-blob` consults the public Rekor transparency log and
`gh attestation verify` calls the GitHub API, so both fail on an isolated
network.

For an air-gapped check, do the verification once on a connected host and carry
the result across, or verify offline with the transparency-log bundle:

```sh
# On a connected host, capture the bundle alongside the artifacts:
cosign verify-blob checksums.txt \
  --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/ftarasenko/go-openstackclient/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle checksums.txt.bundle

# Inside the enclave, verify from the bundle without contacting Rekor:
cosign verify-blob checksums.txt --bundle checksums.txt.bundle --offline
```

`gh attestation verify` accepts `--bundle` for the same purpose; download the
attestation bundle on the connected side and pass it in.

Once `checksums.txt` is trusted, `sha256sum -c checksums.txt` is fully offline
and is what you actually gate the install on.

### Re-deriving the binary yourself

Builds are byte-reproducible: the binary timestamp, the archive member
timestamps and the package timestamps are all pinned to the commit. Two builds
of the same tag produce identical checksums, so you can rebuild from source and
compare against `checksums.txt` rather than trusting the published bytes at all.
This also means a release whose binaries were later pruned to save storage can
still be reconstructed from its tag and checked against the retained checksums.

## Known trade-offs

These are deliberate, documented, and not vulnerabilities — but you should know
about them:

- `--insecure` / `OS_INSECURE` disables TLS certificate verification. It warns
  on stderr on every path that honours it. Do not use it against anything you
  care about.
- `--creds-from-ns` falls back to plain `http://` when the Ironic resource
  advertises no TLS certificate, and warns when it does. Basic-auth credentials
  then cross the network in cleartext.
- `koc vault kv get` prints secret values in cleartext by design; that is the
  command's purpose. `koc vault kv export` writes `0600` and encrypts to a
  recipient key.
- Passing a secret as a command-line flag exposes it to other users via the
  process table on most systems. Prefer the environment or a credential source.
