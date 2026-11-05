package baremetal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
)

// Ironic does not answer a request for a feature newer than the cloud with a
// helpful error, and which error it gives depends on how the feature was
// added:
//
//   - a new route (GET /v1/nodes/<n>/firmware) is not registered below its
//     microversion, so an older ironic answers 404, the same as "no such node";
//   - a new value for an existing field (target: service) fails validation:
//     400, about the value rather than the version;
//   - a write to a route versioned as a whole (POST /v1/inspection_rules): 405;
//   - only an explicit pin above the endpoint's maximum yields the 406 that
//     names the problem.
//
// The functions of a gated feature turn those into a *MicroversionError when
// they can rule the ordinary meaning out.

// Feature is an ironic API feature added after Zed (ironic 21.1, API 1.80),
// the oldest cloud koc supports.
type Feature string

// The gated features. Microversions are from ironic's
// api/controllers/v1/versions.py, releases from common/release_mappings.py.
const (
	FeatureNodeInventory      Feature = "node inventory"
	FeatureShards             Feature = "shards"
	FeatureNodeChildren       Feature = "node children"
	FeatureNodeUnhold         Feature = "node unhold"
	FeatureNodeFirmware       Feature = "node firmware"
	FeatureNodeService        Feature = "node service"
	FeaturePortName           Feature = "port name"
	FeatureNodeVmedia         Feature = "node virtual media"
	FeatureRunbooks           Feature = "runbooks"
	FeatureInspectionRules    Feature = "inspection rules"
	FeaturePortGroupPhysnet   Feature = "port group physical network"
	FeaturePortGroupCategory  Feature = "port group category"
	FeatureRunbookDescription Feature = "runbook description"
	FeatureRunbookTraits      Feature = "runbook traits"
)

// Microversion is the ironic API version f appeared in; "" for an unknown
// feature.
func (f Feature) Microversion() string {
	v, _ := f.introduced()
	return v
}

// Release is the first OpenStack release that ships f.
func (f Feature) Release() string {
	_, r := f.introduced()
	return r
}

func (f Feature) introduced() (version, release string) {
	switch f {
	case FeatureNodeInventory:
		return "1.81", "OpenStack 2023.1"
	case FeatureShards:
		return "1.82", "OpenStack 2023.1"
	case FeatureNodeChildren:
		return "1.83", "OpenStack 2023.2"
	case FeatureNodeUnhold:
		return "1.85", "OpenStack 2023.2"
	case FeatureNodeFirmware:
		return "1.86", "OpenStack 2023.2"
	case FeatureNodeService:
		return "1.87", "OpenStack 2023.2"
	case FeaturePortName:
		return "1.88", "OpenStack 2024.1"
	case FeatureNodeVmedia:
		return "1.89", "OpenStack 2024.1"
	case FeatureRunbooks:
		return "1.92", "OpenStack 2024.2"
	case FeatureInspectionRules:
		return "1.96", "OpenStack 2025.1"
	case FeaturePortGroupPhysnet:
		return "1.102", "OpenStack 2026.1"
	case FeaturePortGroupCategory:
		return "1.103", "OpenStack 2026.1"
	case FeatureRunbookDescription, FeatureRunbookTraits:
		return "1.112", "OpenStack 2026.2"
	}
	return "", ""
}

// MicroversionError reports a request that failed because its feature needs
// a newer ironic API than the client pins or the cloud serves. Err is the
// API's own error.
type MicroversionError struct {
	Feature Feature
	// Pinned is the client's microversion when it is pinned below the
	// feature's; it is checked first, as the caller's own doing.
	Pinned string
	// CloudMax is the endpoint's maximum microversion, "" when it could not
	// be read.
	CloudMax string
	Err      error
}

func (e *MicroversionError) Error() string {
	f := e.Feature
	switch {
	case e.Pinned != "":
		return fmt.Sprintf("%s requires ironic API %s (%s), but the client pins %s: %v",
			f, f.Microversion(), f.Release(), e.Pinned, e.Err)
	case e.CloudMax == "":
		return fmt.Sprintf("%s requires ironic API %s (%s), which this cloud may not support: %v",
			f, f.Microversion(), f.Release(), e.Err)
	}
	return fmt.Sprintf("%s requires ironic API %s (%s); this cloud supports up to %s: %v",
		f, f.Microversion(), f.Release(), e.CloudMax, e.Err)
}

func (e *MicroversionError) Unwrap() error { return e.Err }

// MaxVersionHeader is set by ironic on every response, errors included.
const MaxVersionHeader = "X-OpenStack-Ironic-API-Maximum-Version"

// ExplainMicroversion returns err as a *MicroversionError when it is the
// 400, 404, 405 or 406 a gated feature gets from a client or cloud below the
// feature's microversion, and err unchanged otherwise. When the cloud's
// maximum is known and already high enough, the status means what it usually
// means (no such node, a bad argument), so the claim is made only when that
// alternative is ruled out.
func ExplainMicroversion(ctx context.Context, c *gophercloud.ServiceClient, f Feature, err error) error {
	var explained *MicroversionError
	if err == nil || errors.As(err, &explained) {
		return err
	}
	var unexpected gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &unexpected) {
		return err
	}
	switch unexpected.Actual {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotAcceptable:
	default:
		return err
	}
	// A pin below the feature's floor makes the same unroutable request as an
	// old cloud, and ironic answers it the same way.
	if pinned := c.Microversion; pinned != "" && pinned != "latest" &&
		CompareMicroversions(pinned, f.Microversion()) < 0 {
		return &MicroversionError{Feature: f, Pinned: pinned, Err: err}
	}
	cloudMax := unexpected.ResponseHeader.Get(MaxVersionHeader)
	if cloudMax == "" {
		cloudMax = maxVersion(ctx, c)
	}
	if cloudMax != "" && CompareMicroversions(cloudMax, f.Microversion()) >= 0 {
		return err
	}
	return &MicroversionError{Feature: f, CloudMax: cloudMax, Err: err}
}

// maxVersion reads the maximum microversion from the v1 version document, or
// "" on any failure: it runs on an error path, where a second failure must
// not replace the first.
func maxVersion(ctx context.Context, c *gophercloud.ServiceClient) string {
	var doc struct {
		Version struct {
			Version string `json:"version"`
		} `json:"version"`
	}
	resp, err := c.Get(ctx, c.ServiceURL(), &doc, &gophercloud.RequestOpts{OkCodes: []int{http.StatusOK}})
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return ""
	}
	return doc.Version.Version
}

// CompareMicroversions orders two "major.minor" strings, returning -1, 0 or
// 1. An unparseable version sorts below anything.
func CompareMicroversions(a, b string) int {
	amaj, amin := parseMicroversion(a)
	bmaj, bmin := parseMicroversion(b)
	switch {
	case amaj != bmaj:
		if amaj < bmaj {
			return -1
		}
		return 1
	case amin != bmin:
		if amin < bmin {
			return -1
		}
		return 1
	}
	return 0
}

func parseMicroversion(s string) (major, minor int) {
	majorPart, minorPart, ok := strings.Cut(strings.TrimSpace(s), ".")
	if !ok {
		return -1, -1
	}
	major, err := strconv.Atoi(majorPart)
	if err != nil {
		return -1, -1
	}
	minor, err = strconv.Atoi(minorPart)
	if err != nil {
		return -1, -1
	}
	return major, minor
}
