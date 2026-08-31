package s3cli

import (
	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/auth"
	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// s3Long documents the credential sources on the group itself. The connection
// flags are this group's primary input and are registered here rather than
// globally, so this is the only place they are discoverable.
const s3Long = `List buckets and objects in an S3-compatible store, and move files in and out.

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
a store fronted by wildcard DNS.`

const s3Example = `  # Everything the credentials can see
  koc s3 bucket list

  # The MariaDB backups, oldest first
  koc s3 object list db-backups --sort-column "Last Modified"

  # Fetch one backup and its checksum
  koc s3 download db-backups/<key> ./dump.sql.gz
  koc s3 download db-backups/<key>.sha256 -

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
	return cmd
}
