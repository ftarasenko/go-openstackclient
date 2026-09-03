package server

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
)

// Block-device mapping and the placement fields for "server create".
//
// nova's block_device_mapping_v2 is the only way to give a guest more than one
// disk in a single create call. Without it a multi-volume guest costs one
// "server add volume" per extra disk, and a batch of those walks straight into
// max_io_ops_per_host.
//
// The entries are assembled as maps rather than as servers.BlockDevice values
// because the typed struct cannot express the whole mapping: it has no
// device_name (which the legacy --block-device-mapping form names) and no
// no_device, and its BootIndex field carries no `omitempty`, so every entry it
// builds claims boot index 0 — which nova rejects as a second root device the
// moment a data volume is added. serverCreateOptsExt splices the maps into the
// request body.

// bdmSourceTypes and bdmDestinationTypes are nova's enums for one
// block_device_mapping_v2 entry
// (nova/api/openstack/compute/schemas/block_device_mapping.py).
var (
	bdmSourceTypes      = []string{"volume", "snapshot", "image", "blank"}
	bdmDestinationTypes = []string{"volume", "local"}
)

// bdmDefaultDestination is the destination_type koc supplies when a mapping
// names a source_type but no destination. nova leaves the field optional and
// then defaults it deep inside the compute API, where a wrong guess surfaces as
// an instance that booted with the wrong disk layout; stating it in the request
// keeps the mapping explicit and the failure readable.
var bdmDefaultDestination = map[string]string{
	"volume":   "volume",
	"snapshot": "volume",
	"image":    "volume",
	"blank":    "local",
}

// blockDeviceStringKeys, blockDeviceIntKeys and blockDeviceBoolKeys partition
// the keys --block-device accepts by the type nova wants in the body. They are
// tables rather than one switch so the per-key parsing stays flat.
var (
	blockDeviceStringKeys = []string{
		"uuid", "source_type", "destination_type", "disk_bus",
		"device_name", "device_type", "guest_format", "volume_type", "tag",
	}
	blockDeviceIntKeys  = []string{"boot_index", "volume_size"}
	blockDeviceBoolKeys = []string{"delete_on_termination", "no_device"}
)

// blockDeviceKeys lists every accepted key, for the flag help and error text.
func blockDeviceKeys() string {
	all := slices.Concat(blockDeviceStringKeys, blockDeviceIntKeys, blockDeviceBoolKeys)
	slices.Sort(all)
	return strings.Join(all, ", ")
}

// parseBlockDevice parses one upstream-OSC `--block-device` value: a
// comma-separated list of the block_device_mapping_v2 key=value pairs, e.g.
// "source_type=volume,uuid=<vol>,destination_type=volume". Hyphens in a key are
// accepted as underscores ("delete-on-termination"), since every other koc
// key=value flag spells its keys with hyphens.
func parseBlockDevice(s string) (map[string]any, error) {
	bdm := map[string]any{}
	for _, part := range strings.Split(s, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		k, v, err := parseKeyVal(part)
		if err != nil {
			return nil, fmt.Errorf("invalid --block-device %q: %w", s, err)
		}
		key := strings.ReplaceAll(strings.TrimSpace(k), "-", "_")
		if err := setBlockDeviceKey(bdm, key, strings.TrimSpace(v)); err != nil {
			return nil, fmt.Errorf("invalid --block-device %q: %w", s, err)
		}
	}
	if len(bdm) == 0 {
		return nil, fmt.Errorf("invalid --block-device %q: expected key=value pairs (%s)", s, blockDeviceKeys())
	}
	return finishBlockDevice(bdm, s)
}

// setBlockDeviceKey stores one key=value pair in bdm, converting the value to
// the JSON type nova's schema requires.
func setBlockDeviceKey(bdm map[string]any, key, val string) error {
	switch {
	case slices.Contains(blockDeviceStringKeys, key):
		bdm[key] = val
	case slices.Contains(blockDeviceIntKeys, key):
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("%s must be an integer, got %q", key, val)
		}
		bdm[key] = n
	case slices.Contains(blockDeviceBoolKeys, key):
		b, err := parseBDMBool(val)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		bdm[key] = b
	default:
		return fmt.Errorf("unknown key %q: want one of %s", key, blockDeviceKeys())
	}
	return nil
}

// parseBDMBool reads the boolean spellings the OSC block-device flags accept.
// strconv.ParseBool covers true/false/1/0/t/f; yes/no are added because the
// legacy --block-device-mapping form is routinely written with them.
func parseBDMBool(val string) (bool, error) {
	switch strings.ToLower(val) {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	}
	b, err := strconv.ParseBool(val)
	if err != nil {
		return false, fmt.Errorf("expected true or false, got %q", val)
	}
	return b, nil
}

// finishBlockDevice validates a parsed mapping and fills in the source and
// destination defaults. A no_device entry is a "suppress this device" marker
// rather than a real mapping, so it is exempt from both.
func finishBlockDevice(bdm map[string]any, raw string) (map[string]any, error) {
	if no, ok := bdm["no_device"].(bool); ok && no {
		return bdm, nil
	}
	src, _ := bdm["source_type"].(string)
	if src == "" {
		// A mapping that names a uuid points at an existing volume unless it says
		// otherwise; one that names none can only be a blank device.
		src = "blank"
		if _, named := bdm["uuid"]; named {
			src = "volume"
		}
		bdm["source_type"] = src
	}
	if !slices.Contains(bdmSourceTypes, src) {
		return nil, fmt.Errorf("invalid --block-device %q: source_type must be one of %s",
			raw, strings.Join(bdmSourceTypes, ", "))
	}
	dst, _ := bdm["destination_type"].(string)
	if dst == "" {
		dst = bdmDefaultDestination[src]
		bdm["destination_type"] = dst
	}
	if !slices.Contains(bdmDestinationTypes, dst) {
		return nil, fmt.Errorf("invalid --block-device %q: destination_type must be one of %s",
			raw, strings.Join(bdmDestinationTypes, ", "))
	}
	if uuid, _ := bdm["uuid"].(string); src != "blank" && uuid == "" {
		return nil, fmt.Errorf("invalid --block-device %q: source_type=%s needs uuid=<id>", raw, src)
	}
	if _, sized := bdm["volume_size"]; src == "blank" && !sized {
		return nil, fmt.Errorf("invalid --block-device %q: source_type=blank needs volume_size=<GB>", raw)
	}
	return bdm, nil
}

// parseBlockDeviceMapping parses one value of upstream OSC's legacy
// `--block-device-mapping` flag:
//
//	<dev-name>=<id>[:<type>[:<size-GB>[:<delete-on-terminate>]]]
//
// where <type> is volume (the default), snapshot or image. The resulting
// mapping always lands on a cinder volume and carries no boot_index, so it is a
// data disk rather than a second root device.
func parseBlockDeviceMapping(s string) (map[string]any, error) {
	const usage = "want <dev-name>=<id>[:<type>[:<size-GB>[:<delete-on-terminate>]]]"
	dev, spec, err := parseKeyVal(s)
	if err != nil {
		return nil, fmt.Errorf("invalid --block-device-mapping %q: %s", s, usage)
	}
	parts := strings.Split(spec, ":")
	if len(parts) > 4 {
		return nil, fmt.Errorf("invalid --block-device-mapping %q: %s", s, usage)
	}
	if parts[0] == "" {
		return nil, fmt.Errorf("invalid --block-device-mapping %q: missing the volume, snapshot or image id", s)
	}
	bdm := map[string]any{"device_name": dev, "uuid": parts[0], "destination_type": "volume"}
	src := "volume"
	if len(parts) > 1 && parts[1] != "" {
		src = parts[1]
	}
	if !slices.Contains(bdmSourceTypes, src) || src == "blank" {
		return nil, fmt.Errorf("invalid --block-device-mapping %q: type must be volume, snapshot or image", s)
	}
	bdm["source_type"] = src
	if len(parts) > 2 && parts[2] != "" {
		size, err := strconv.Atoi(parts[2])
		if err != nil {
			return nil, fmt.Errorf("invalid --block-device-mapping %q: size must be an integer, got %q", s, parts[2])
		}
		bdm["volume_size"] = size
	}
	if len(parts) > 3 && parts[3] != "" {
		del, err := parseBDMBool(parts[3])
		if err != nil {
			return nil, fmt.Errorf("invalid --block-device-mapping %q: delete-on-terminate: %w", s, err)
		}
		bdm["delete_on_termination"] = del
	}
	return bdm, nil
}

// serverCreateOptsExt carries the create fields gophercloud's servers.CreateOpts
// cannot express: nova's 2.74 `host` (the struct models only
// hypervisor_hostname) and the block_device_mapping_v2 list, which is assembled
// as maps for the reasons at the top of this file.
type serverCreateOptsExt struct {
	servers.CreateOptsBuilder
	Host        string
	BlockDevice []map[string]any
}

func (opts serverCreateOptsExt) ToServerCreateMap() (map[string]any, error) {
	body, err := opts.CreateOptsBuilder.ToServerCreateMap()
	if err != nil {
		return nil, err
	}
	if opts.Host == "" && len(opts.BlockDevice) == 0 {
		return body, nil
	}
	srv, ok := body["server"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`server create body carries no "server" object`)
	}
	if opts.Host != "" {
		srv["host"] = opts.Host
	}
	if len(opts.BlockDevice) > 0 {
		srv["block_device_mapping_v2"] = opts.BlockDevice
	}
	return body, nil
}

// bootsFromBlockDevice reports whether any mapping claims boot index 0, i.e.
// whether the request already names its own root device. nova rejects a
// top-level imageRef alongside one (it would be a second root), so the caller
// drops the imageRef in that case — the same move --boot-from-volume makes.
func bootsFromBlockDevice(bdms []map[string]any) bool {
	for _, bdm := range bdms {
		if idx, ok := bdm["boot_index"].(int); ok && idx == 0 {
			return true
		}
	}
	return false
}
