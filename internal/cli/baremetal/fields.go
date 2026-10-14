package baremetal

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/pagination"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// Ironic resources are rendered from the raw response body rather than from
// gophercloud's structs: those lag the API (no port name, internal_info,
// category or vendor; no node health or instance_name), and a field missing
// from the struct silently disappears from `show`. Labels and field order are
// python-ironicclient's (ironicclient/v1/resource_fields.py).

// fieldLabels maps an ironic field to python-ironicclient's column label.
var fieldLabels = map[string]string{
	"actions":                         "Actions",
	"address":                         "Address",
	"allocation_uuid":                 "Allocation UUID",
	"automated_clean":                 "Automated Clean",
	"bios_interface":                  "BIOS Interface",
	"boot_index":                      "Boot Index",
	"boot_interface":                  "Boot Interface",
	"boot_mode":                       "Boot Mode",
	"category":                        "Category",
	"chassis_uuid":                    "Chassis UUID",
	"clean_step":                      "Clean Step",
	"conditions":                      "Conditions",
	"conductor":                       "Conductor",
	"conductor_group":                 "Conductor Group",
	"connector_id":                    "Connector ID",
	"console_enabled":                 "Console Enabled",
	"console_interface":               "Console Interface",
	"created_at":                      "Created At",
	"deploy_interface":                "Deploy Interface",
	"deploy_step":                     "Deploy Step",
	"description":                     "Description",
	"disable_power_off":               "Disable Power Off",
	"disable_ramdisk":                 "Disable Ramdisk",
	"driver":                          "Driver",
	"driver_info":                     "Driver Info",
	"driver_internal_info":            "Driver Internal Info",
	"extra":                           "Extra",
	"fault":                           "Fault",
	"firmware_interface":              "Firmware Interface",
	"health":                          "Health",
	"inspect_interface":               "Inspect Interface",
	"inspection_finished_at":          "Inspection Finished At",
	"inspection_started_at":           "Inspection Started At",
	"instance_info":                   "Instance Info",
	"instance_name":                   "Instance Name",
	"instance_uuid":                   "Instance UUID",
	"internal_info":                   "Internal Info",
	"is_smartnic":                     "Is Smart NIC port",
	"last_error":                      "Last Error",
	"lessee":                          "Lessee",
	"local_link_connection":           "Local Link Connection",
	"maintenance":                     "Maintenance",
	"maintenance_reason":              "Maintenance Reason",
	"management_interface":            "Management Interface",
	"mode":                            "Mode",
	"name":                            "Name",
	"network_data":                    "Network Configuration",
	"network_interface":               "Network Interface",
	"node_uuid":                       "Node UUID",
	"owner":                           "Owner",
	"parent_node":                     "Parent Node",
	"phase":                           "Phase",
	"physical_network":                "Physical Network",
	"portgroup_uuid":                  "Portgroup UUID",
	"power_interface":                 "Power Interface",
	"power_state":                     "Power State",
	"priority":                        "Priority",
	"properties":                      "Properties",
	"protected":                       "Protected",
	"protected_reason":                "Protected Reason",
	"provision_state":                 "Provisioning State",
	"provision_updated_at":            "Provision Updated At",
	"public":                          "Public",
	"pxe_enabled":                     "PXE boot enabled",
	"raid_config":                     "Current RAID configuration",
	"raid_interface":                  "RAID Interface",
	"rescue_interface":                "Rescue Interface",
	"reservation":                     "Reservation",
	"resource_class":                  "Resource Class",
	"retired":                         "Retired",
	"retired_reason":                  "Retired Reason",
	"secure_boot":                     "Secure Boot",
	"sensitive":                       "Sensitive",
	"standalone_ports_supported":      "Standalone Ports Supported",
	"steps":                           "Steps",
	"storage_interface":               "Storage Interface",
	"target_power_state":              "Target Power State",
	"target_provision_state":          "Target Provision State",
	"target_raid_config":              "Target RAID configuration",
	"traits":                          "Traits",
	"type":                            "Type",
	"updated_at":                      "Updated At",
	"uuid":                            "UUID",
	"vendor":                          "Vendor",
	"vendor_interface":                "Vendor Interface",
	"volume_id":                       "Volume ID",
	"volume_type":                     "Driver Volume Type",
	"available_for_dynamic_portgroup": "Available For Dynamic Portgroup",
	"dynamic_portgroup":               "Dynamic Portgroup",
}

// label returns the column label for an ironic field, falling back to the
// field name for one python-ironicclient does not know.
func label(field string) string {
	if l, ok := fieldLabels[field]; ok {
		return l
	}
	return field
}

// labels maps field names to their column labels.
func labels(fields []string) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = label(f)
	}
	return out
}

// navigationKeys are the link-only members of an ironic resource, dropped from
// `show` as python-ironicclient drops them.
var navigationKeys = []string{"links", "ports", "portgroups", "states", "volume", "nodes"}

// showFields renders a resource body the way python-ironicclient's `show`
// does: every field the API returned, sorted by name, minus the navigation
// links. known fields are always present, null when this API version does not
// return them, so `-c <field>` names a stable column on every cloud.
func showFields(body map[string]any, known []string) ([]string, []any) {
	seen := make(map[string]bool, len(body)+len(known))
	var fields []string
	for _, k := range known {
		if !seen[k] {
			seen[k] = true
			fields = append(fields, k)
		}
	}
	for k := range body {
		if !seen[k] && !slices.Contains(navigationKeys, k) {
			seen[k] = true
			fields = append(fields, k)
		}
	}
	slices.Sort(fields)
	values := make([]any, len(fields))
	for i, f := range fields {
		values[i] = body[f]
	}
	return fields, values
}

// knownColumns is the column set a write verb checks -c against: the known
// fields, as showFields renders them.
func knownColumns(known []string) []string {
	cols, _ := showFields(nil, known)
	return cols
}

// listTable renders resource bodies as a table of the given fields, headed by
// python-ironicclient's labels.
func listTable(rows []map[string]any, fields []string) output.Table {
	t := output.Table{Columns: labels(fields), Rows: make([][]any, 0, len(rows))}
	for _, r := range rows {
		row := make([]any, len(fields))
		for i, f := range fields {
			row[i] = r[f]
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// resultMap decodes a gophercloud result's body into a map.
func resultMap(r gophercloud.Result) (map[string]any, error) {
	if r.Err != nil {
		return nil, r.Err
	}
	var m map[string]any
	if err := r.ExtractInto(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// pageMaps extracts the resources under key from a list page.
func pageMaps(key string) func(pagination.Page) ([]map[string]any, error) {
	return func(p pagination.Page) ([]map[string]any, error) {
		var v map[string][]map[string]any
		if err := decodeInto(p.GetBody(), &v); err != nil {
			return nil, err
		}
		return v[key], nil
	}
}

// decodeInto round-trips v through JSON into out; used to read a typed view of
// a resource body.
func decodeInto(v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decoding %s: %w", strings.TrimPrefix(fmt.Sprintf("%T", out), "*"), err)
	}
	return nil
}
