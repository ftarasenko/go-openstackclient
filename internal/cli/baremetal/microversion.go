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

// koc supports OpenStack Zed (2022.2) and newer — see AGENTS.md, "Minimum
// supported cloud". Zed ships ironic 21.4.0, whose API tops out at 1.82, so
// every feature ironic gated behind a later microversion is missing on the
// oldest clouds in the fleet.
//
// Ironic does not answer such a request with a helpful error, and which error
// it does give depends on how the feature was added:
//
//   - a new route (`GET /v1/nodes/<n>/firmware`) is simply not registered below
//     its microversion, so Zed answers **404** — indistinguishable from "no such
//     node";
//   - a new value for an existing field (`target: service`) fails validation, so
//     Zed answers **400**, complaining about the value rather than the version;
//   - only an explicitly pinned request above the endpoint's maximum yields the
//     **406** that actually names the problem, and koc's defaults negotiate
//     "latest" precisely so that never happens.
//
// All three arrive through gophercloud as an opaque status code.
// explainMicroversion turns them into the real answer. Callers wrap the API
// error of a gated command and nothing else.

// ironicFeature names a command and the microversion its endpoint appeared in.
// The release is carried alongside the number because "1.86" means nothing to an
// operator deciding whether to upgrade.
type ironicFeature struct {
	command string
	min     string
	release string
}

// Features gated above the Zed cap of 1.82. Microversions are from
// ironic/api/controllers/v1/versions.py; the release mapping is that file's
// MINOR_MAX_VERSION read across the stable series (21.4 → 1.82, 22.0 → 1.83,
// 23.0 → 1.87, 24.1 → 1.90, 26.1 → 1.93, 29.0 → 1.96).
var (
	featureNodeUnhold   = ironicFeature{"baremetal node unhold", "1.85", releaseBobcat}
	featureNodeFirmware = ironicFeature{"baremetal node firmware list", "1.86", releaseBobcat}
	featureNodeService  = ironicFeature{"baremetal node service", "1.87", releaseBobcat}
	featurePortName     = ironicFeature{"baremetal port --name", "1.88", "OpenStack 2024.1"}
)

// ironicMaxVersionHeader is set by ironic on every response, including errors.
const ironicMaxVersionHeader = "X-OpenStack-Ironic-API-Maximum-Version"

// explainMicroversion rewrites the 400/404/406 a version-gated feature returns
// on an older cloud into a message naming the requirement, and leaves every
// other error untouched.
//
// It only makes the claim when it can rule the alternative out: if the
// endpoint's maximum version is discoverable and already at or above the
// feature's minimum, then the status code means what it usually means — no such
// node, bad argument — and the original error is returned unchanged. That check
// is what makes it safe to react to a 400.
func explainMicroversion(ctx context.Context, client *gophercloud.ServiceClient, f ironicFeature, err error) error {
	if err == nil {
		return nil
	}
	var unexpected gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &unexpected) {
		return err
	}
	switch unexpected.Actual {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusNotAcceptable:
	default:
		return err
	}

	// An explicitly pinned microversion below the feature's floor produces the
	// same unroutable request as an old cloud, and ironic answers it the same
	// way — a 404 whose description is empty, which is strictly less informative
	// than the "Node ... could not be found" a real missing node gets. The pin is
	// checked first because it is the operator's own doing and can be named
	// exactly.
	if pinned := client.Microversion; pinned != "" && pinned != "latest" &&
		compareMicroversions(pinned, f.min) < 0 {
		return fmt.Errorf("%s requires ironic API %s (%s), but --os-baremetal-api-version pins %s: %w",
			f.command, f.min, f.release, pinned, err)
	}

	// Prefer the header ironic already sent; fall back to the version document
	// only when it is absent. Either way this costs nothing on the happy path.
	cloudMax := unexpected.ResponseHeader.Get(ironicMaxVersionHeader)
	if cloudMax == "" {
		cloudMax = ironicMaxVersion(ctx, client)
	}
	if cloudMax == "" {
		return fmt.Errorf("%s requires ironic API %s (%s), which this cloud may not support: %w",
			f.command, f.min, f.release, err)
	}
	if compareMicroversions(cloudMax, f.min) >= 0 {
		return err
	}
	return fmt.Errorf("%s requires ironic API %s (%s); this cloud supports up to %s: %w",
		f.command, f.min, f.release, cloudMax, err)
}

// ironicMaxVersion reads the maximum microversion from the endpoint's v1 version
// document. It returns "" on any failure — this runs on an error path, where a
// second failure must not replace the caller's original error.
func ironicMaxVersion(ctx context.Context, client *gophercloud.ServiceClient) string {
	var doc struct {
		Version struct {
			Version string `json:"version"`
		} `json:"version"`
	}
	resp, err := client.Get(ctx, client.ServiceURL(), &doc, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return ""
	}
	return doc.Version.Version
}

// compareMicroversions orders two "major.minor" strings, returning -1, 0 or 1.
// An unparseable version sorts as lower than anything, so a malformed value can
// never suppress the explanation.
func compareMicroversions(a, b string) int {
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
