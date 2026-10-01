package server

import (
	"strings"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// serverCreateColumns is every column `server create` can render: what
// `server show` prints for a nova server at any microversion up to the newest
// (after serverFieldAliases), plus the adminPass the create response carries.
// It exists so -c can be checked before the create is sent; the real set comes
// from nova's response, which is too late.
var serverCreateColumns = []string{
	"accessIPv4",
	"accessIPv6",
	"addresses",
	"adminPass",
	"config_drive",
	"created",
	"description",
	"fault",
	"flavor",
	"hostId",
	"host_status",
	"id",
	"image",
	"key_name",
	"locked",
	"locked_reason",
	"name",
	"OS-DCF:diskConfig",
	"OS-EXT-AZ:availability_zone",
	"OS-EXT-SRV-ATTR:host",
	"OS-EXT-SRV-ATTR:hostname",
	"OS-EXT-SRV-ATTR:hypervisor_hostname",
	"OS-EXT-SRV-ATTR:instance_name",
	"OS-EXT-SRV-ATTR:kernel_id",
	"OS-EXT-SRV-ATTR:launch_index",
	"OS-EXT-SRV-ATTR:ramdisk_id",
	"OS-EXT-SRV-ATTR:reservation_id",
	"OS-EXT-SRV-ATTR:root_device_name",
	"OS-EXT-SRV-ATTR:user_data",
	"OS-EXT-STS:power_state",
	"OS-EXT-STS:task_state",
	"OS-EXT-STS:vm_state",
	"OS-SRV-USG:launched_at",
	"OS-SRV-USG:terminated_at",
	"pinned_availability_zone",
	"progress",
	"project_id",
	"properties",
	"scheduler_hints",
	"security_groups",
	"server_groups",
	"status",
	"tags",
	"trusted_image_certificates",
	"updated",
	"user_id",
	"volumes_attached",
}

// serverCreateColumnAliases are the headers koc's create summary used before it
// rendered the whole server, kept so scripts selecting them still work.
var serverCreateColumnAliases = map[string]string{
	"networks":       "addresses",
	"admin password": "adminPass",
}

// aliasServerCreateColumns rewrites -c names given in the old spelling.
func aliasServerCreateColumns(o *output.Options) {
	for i, c := range o.Columns {
		if to, ok := serverCreateColumnAliases[strings.ToLower(strings.TrimSpace(c))]; ok {
			o.Columns[i] = to
		}
	}
}

// containsFold reports whether list holds s, ignoring case and surrounding
// space as -c/--column matching does.
func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

// canonicalColumn returns the catalog's spelling of the column s names.
func canonicalColumn(catalog []string, s string) string {
	for _, v := range catalog {
		if strings.EqualFold(v, strings.TrimSpace(s)) {
			return v
		}
	}
	return s
}
