package placement

import (
	"context"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
)

// koc asks placement for "latest" by default (see auth/services.go), which the
// service resolves per-request and which keeps every command on the newest
// behaviour the endpoint offers. That is the right default for the wire, but it
// is not a microversion *number*, and a little gophercloud code reads
// client.Microversion and parses it rather than just sending it:
// resourceproviders.UpdateAggregates switches the request body shape on it,
// because placement stopped enveloping the aggregate list at 1.19.
//
// Given "latest" that parse fails, and the command dies with
// `invalid microversion format: "latest"` before it ever reaches the network.
// concreteClient converts the negotiated default into the number it stands for
// so that code can work.
//
// The lookup is only performed when the microversion is not already numeric, so
// an operator who pinned --os-placement-api-version pays nothing.

// concreteClient returns a client whose Microversion is a "major.minor" number.
//
// When the microversion is already numeric the original client is returned
// untouched. Otherwise the endpoint's maximum is read from its version document
// and applied to a shallow copy — a copy because the ProviderClient is shared,
// and setMicroversionHeader rewrites the header from client.Microversion on
// every request, so mutating the caller's client would leak into later calls
// (the same reason server/actions.go copies before pinning nova 2.43).
//
// A failed lookup returns the client unchanged rather than an error: the caller
// is about to make a real request that will report a real failure, and a
// version-document hiccup must not replace that with a worse message.
func concreteClient(ctx context.Context, client *gophercloud.ServiceClient) *gophercloud.ServiceClient {
	if isNumericMicroversion(client.Microversion) {
		return client
	}
	maxVersion := placementMaxVersion(ctx, client)
	if maxVersion == "" {
		return client
	}
	pinned := *client
	pinned.Microversion = maxVersion
	return &pinned
}

// placementMaxVersion reads max_version from the endpoint's version document,
// returning "" on any failure.
func placementMaxVersion(ctx context.Context, client *gophercloud.ServiceClient) string {
	var doc struct {
		Versions []struct {
			MaxVersion string `json:"max_version"`
		} `json:"versions"`
	}
	resp, err := client.Get(ctx, client.ServiceURL(), &doc, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil || len(doc.Versions) == 0 {
		return ""
	}
	return doc.Versions[0].MaxVersion
}

// compareMicroversions orders two "major.minor" strings, returning -1, 0 or 1.
// An unparseable version sorts below anything.
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

// isNumericMicroversion reports whether s is a "major.minor" version rather than
// a symbolic one such as "latest" (or empty).
func isNumericMicroversion(s string) bool {
	if s == "" {
		return false
	}
	seenDot := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r == '.' && !seenDot:
			seenDot = true
		default:
			return false
		}
	}
	return seenDot
}
