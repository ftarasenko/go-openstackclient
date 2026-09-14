package s3cli

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// s3Long documents the credential sources on the group itself. The connection
// flags are this group's primary input and are registered here rather than
// globally, so this is the only place they are discoverable.
const s3Long = `Manage buckets and objects in an S3-compatible store, and move data in and out.

This group is koc-specific (S3 is not an OpenStack service, and upstream's
object-store commands speak Swift) and does not authenticate against Keystone:
only S3 credentials are used. It is aimed at the LCM cluster's Garage, which
holds GitLab's object storage and the MariaDB dumps the "backup-db" scheduled
pipeline uploads.

Credentials come from, in order of precedence:

  --s3-endpoint, --s3-access-key, --s3-secret-key, --s3-region
  the environment: AWS_ENDPOINT_URL / AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
    / AWS_REGION, the S3_* spellings, or the lower-case s3_host / s3_access_key
    / s3_secret_key / s3_region the KeyStack installer sets as GitLab group
    CI/CD variables — so a pipeline job needs no flags
  --s3-creds-from-ns <namespace>[/<secret>][:<key>], reading them from a
    Kubernetes Secret

Prefer the environment to --s3-secret-key: a flag value is visible in the
process list and in shell history.

On a KeyStack LCM cluster the keys are not all reachable the same way. GitLab's
key is a Kubernetes Secret, so "--s3-creds-from-ns lcm-gitlab" is enough. The
db-backup key is held only by Garage itself and by the GitLab group CI/CD
variables, so export it from either:

  garage:  kubectl exec -n lcm-garage garage-0 -c garage -- \
             /usr/local/bin/garage key info --show-secret db-backup
  gitlab:  the s3_access_key / s3_secret_key group variables (masked)

Addressing is path-style by default (<endpoint>/<bucket>/<key>), matching the
--host-bucket setting the backup pipeline gives s3cmd; pass --no-path-style for
a store fronted by wildcard DNS.

Every reference accepts either "<bucket>/<key>" or the "s3://<bucket>/<key>"
spelling, so a path copied out of s5cmd or "aws s3" pastes in unchanged, and a
wildcard ("db-backups/2026/*.gz") selects many where a command takes one.

Uploads are multipart past --part-size, so there is no 5 GiB ceiling, and "-" as
the source reads standard input. download, upload, copy, move and object delete
all take --recursive over a whole prefix or tree, --concurrency objects at a
time, with --include/--exclude and --dry-run. copy and move are server-side: the
bytes never travel through koc.

A failed request is retried with backoff (--s3-retries); --s3-anonymous sends
requests unsigned, for a bucket granted to everyone.`

const s3Example = `  # Everything the credentials can see
  koc s3 bucket list

  # The MariaDB backups, oldest first
  koc s3 object list db-backups --sort-column "Last Modified"

  # Fetch one backup and its checksum
  koc s3 download db-backups/<key> ./dump.sql.gz
  koc s3 download db-backups/<key>.sha256 -

  # How much space the backups take, per storage class
  koc s3 du db-backups --group

  # A scratch bucket, and its removal once emptied
  koc s3 bucket create scratch
  koc s3 object delete scratch/ --recursive
  koc s3 bucket delete scratch

  # Stream a dump straight in, no staging on disk
  mysqldump --all-databases | gzip | koc s3 upload - db-backups/nightly.sql.gz

  # Restore a month, four objects at a time
  koc s3 download db-backups/2026/08/ ./restore --recursive --concurrency 4

  # Promote last night's dump, server-side
  koc s3 copy db-backups/nightly-2026-09-13.sql.gz db-backups/latest.sql.gz

  # Hand someone one backup for an hour, without a key
  koc s3 presign db-backups/latest.sql.gz --expire 1h

  # GitLab's own key, straight from the cluster
  koc s3 --s3-creds-from-ns lcm-gitlab bucket list`

// NewCommand builds the "s3" command group.
func NewCommand(a *auth.Options, o *output.Options) *cobra.Command {
	f := &connFlags{}
	cmd := &cobra.Command{
		Use:     "s3",
		Short:   "Work with an S3-compatible object store (koc-specific)",
		Long:    s3Long,
		Example: s3Example,
	}
	f.addTo(cmd.PersistentFlags())

	cmd.AddCommand(newBucketCommand(a, o, f))
	cmd.AddCommand(newObjectCommand(a, o, f))
	cmd.AddCommand(newDownloadCommand(a, o, f))
	cmd.AddCommand(newUploadCommand(a, o, f))
	cmd.AddCommand(newDuCommand(a, o, f))
	cmd.AddCommand(newCopyCommand(a, o, f))
	cmd.AddCommand(newMoveCommand(a, o, f))
	cmd.AddCommand(newPresignCommand(a, o, f))
	return cmd
}
