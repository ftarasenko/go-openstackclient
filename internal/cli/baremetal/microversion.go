package baremetal

import (
	"errors"
	"fmt"
	"strings"

	bmapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/baremetal"
)

// featureCommand names the koc command behind a gated ironic feature, as the
// CLI's microversion errors say it.
func featureCommand(f bmapi.Feature) string {
	switch f {
	case bmapi.FeatureNodeInventory:
		return "baremetal node inventory"
	case bmapi.FeatureShards:
		return "baremetal shard list / node --shard"
	case bmapi.FeatureNodeChildren:
		return "baremetal node children list"
	case bmapi.FeatureNodeUnhold:
		return "baremetal node unhold"
	case bmapi.FeatureNodeFirmware:
		return "baremetal node firmware list"
	case bmapi.FeatureNodeService:
		return "baremetal node service"
	case bmapi.FeaturePortName:
		return "baremetal port --name"
	case bmapi.FeatureNodeVmedia:
		return "baremetal node vmedia"
	case bmapi.FeatureRunbooks:
		return "baremetal runbook"
	case bmapi.FeatureInspectionRules:
		return "baremetal inspection rule"
	case bmapi.FeaturePortGroupPhysnet:
		return "baremetal port group --physical-network"
	case bmapi.FeaturePortGroupCategory:
		return "baremetal port group --category"
	case bmapi.FeatureRunbookDescription:
		return "baremetal runbook --description"
	case bmapi.FeatureRunbookTraits:
		return "baremetal runbook trait"
	}
	return string(f)
}

// explained rewords a *bmapi.MicroversionError in err's chain in the CLI's
// terms: the koc command, and the --os-baremetal-api-version pin. An error
// already reworded is returned as it is.
func explained(err error) error {
	var done *explainedError
	var me *bmapi.MicroversionError
	if errors.As(err, &done) || !errors.As(err, &me) {
		return err
	}
	f := me.Feature
	var msg string
	switch {
	case me.Pinned != "":
		msg = fmt.Sprintf("%s requires ironic API %s (%s), but --os-baremetal-api-version pins %s: %v",
			featureCommand(f), f.Microversion(), f.Release(), me.Pinned, me.Err)
	case me.CloudMax == "":
		msg = fmt.Sprintf("%s requires ironic API %s (%s), which this cloud may not support: %v",
			featureCommand(f), f.Microversion(), f.Release(), me.Err)
	default:
		msg = fmt.Sprintf("%s requires ironic API %s (%s); this cloud supports up to %s: %v",
			featureCommand(f), f.Microversion(), f.Release(), me.CloudMax, me.Err)
	}
	return &explainedError{msg: strings.TrimSuffix(err.Error(), me.Error()) + msg, err: err}
}

type explainedError struct {
	msg string
	err error
}

func (e *explainedError) Error() string { return e.msg }
func (e *explainedError) Unwrap() error { return e.err }
