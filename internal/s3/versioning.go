package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Object versioning. Note that Garage — koc's primary target — does not
// implement it: its GetBucketVersioning is a stub that always answers "not
// enabled", and enabling it fails. These calls are here for the other stores
// koc can be pointed at (AWS, Ceph RGW, MinIO), and for the operator who needs
// to confirm which of the two they are talking to.
const (
	// VersioningEnabled and VersioningSuspended are the two states S3 defines.
	// A bucket that was never configured reports neither, which this package
	// renders as VersioningUnversioned.
	VersioningEnabled     = "Enabled"
	VersioningSuspended   = "Suspended"
	VersioningUnversioned = "Unversioned"
)

// versioningConfiguration is the body of both versioning calls.
type versioningConfiguration struct {
	XMLName xml.Name `xml:"VersioningConfiguration"`
	Status  string   `xml:"Status,omitempty"`
}

// GetBucketVersioning reports the bucket's versioning state, normalising the
// "never configured" empty answer to VersioningUnversioned so a caller never
// has to special-case the empty string.
func (c *Client) GetBucketVersioning(ctx context.Context, bucket string) (string, error) {
	var cfg versioningConfiguration
	if err := c.getXML(ctx, c.url(bucket, "", url.Values{"versioning": {""}}), &cfg); err != nil {
		return "", err
	}
	if cfg.Status == "" {
		return VersioningUnversioned, nil
	}
	return cfg.Status, nil
}

// SetBucketVersioning enables or suspends versioning. status must be
// VersioningEnabled or VersioningSuspended: S3 has no call that returns a
// bucket to never-versioned, which is why VersioningUnversioned is not accepted
// here.
func (c *Client) SetBucketVersioning(ctx context.Context, bucket, status string) error {
	switch status {
	case VersioningEnabled, VersioningSuspended:
	default:
		return fmt.Errorf("versioning status must be %q or %q, not %q",
			VersioningEnabled, VersioningSuspended, status)
	}

	body, err := xml.Marshal(versioningConfiguration{Status: status})
	if err != nil {
		return fmt.Errorf("encoding versioning body: %w", err)
	}
	return c.do(ctx, request{
		method:      http.MethodPut,
		url:         c.url(bucket, "", url.Values{"versioning": {""}}),
		body:        bytes.NewReader(body),
		payloadHash: hexSHA256(body),
		size:        int64(len(body)),
		header: map[string]string{
			hdrContentType: "application/xml",
			"Content-MD5":  contentMD5(body),
		},
	}, drainBody)
}

// listVersionsPage is one ListObjectVersions response.
type listVersionsPage struct {
	IsTruncated         bool   `xml:"IsTruncated"`
	NextKeyMarker       string `xml:"NextKeyMarker"`
	NextVersionIDMarker string `xml:"NextVersionIdMarker"`
	Versions            []struct {
		Key          string `xml:"Key"`
		VersionID    string `xml:"VersionId"`
		IsLatest     bool   `xml:"IsLatest"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		StorageClass string `xml:"StorageClass"`
	} `xml:"Version"`
	DeleteMarkers []struct {
		Key          string `xml:"Key"`
		VersionID    string `xml:"VersionId"`
		IsLatest     bool   `xml:"IsLatest"`
		LastModified string `xml:"LastModified"`
	} `xml:"DeleteMarker"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

// listObjectVersions walks the ?versions endpoint, which pages on a key marker
// plus a version-id marker rather than a continuation token.
//
// A page's <Version> and <DeleteMarker> elements interleave on the wire, and
// decoding them into two slices loses that order, so each page is re-sorted by
// key and then newest-first. The result is the presentation a reader wants
// anyway, and it is deterministic, which the wire order across a page boundary
// is not.
func (c *Client) listObjectVersions(ctx context.Context, bucket string, opts ListOptions,
	fn func(Object) error) error {
	keyMarker, versionMarker, seen := "", "", 0
	for {
		q := url.Values{"versions": {""}, "max-keys": {strconv.Itoa(pageSize(opts.Limit, seen))}}
		if opts.Prefix != "" {
			q.Set("prefix", opts.Prefix)
		}
		if opts.Delimiter != "" {
			q.Set("delimiter", opts.Delimiter)
		}
		if keyMarker != "" {
			q.Set("key-marker", keyMarker)
		}
		if versionMarker != "" {
			q.Set("version-id-marker", versionMarker)
		}

		var page listVersionsPage
		if err := c.getXML(ctx, c.url(bucket, "", q), &page); err != nil {
			return err
		}
		for _, obj := range versionEntries(&page) {
			if err := fn(obj); err != nil {
				return err
			}
			if seen++; opts.Limit > 0 && seen >= opts.Limit {
				return nil
			}
		}
		if !page.IsTruncated || (page.NextKeyMarker == "" && page.NextVersionIDMarker == "") {
			return nil
		}
		keyMarker, versionMarker = page.NextKeyMarker, page.NextVersionIDMarker
	}
}

// versionEntries flattens one page into the order described on
// listObjectVersions.
func versionEntries(page *listVersionsPage) []Object {
	out := make([]Object, 0, len(page.Versions)+len(page.DeleteMarkers)+len(page.CommonPrefixes))
	for _, p := range page.CommonPrefixes {
		out = append(out, Object{Key: p.Prefix, IsPrefix: true})
	}
	for _, v := range page.Versions {
		out = append(out, Object{
			Key:          v.Key,
			Size:         v.Size,
			LastModified: parseS3Time(v.LastModified),
			ETag:         strings.Trim(v.ETag, `"`),
			StorageClass: v.StorageClass,
			VersionID:    v.VersionID,
			IsLatest:     v.IsLatest,
		})
	}
	for _, d := range page.DeleteMarkers {
		out = append(out, Object{
			Key:          d.Key,
			LastModified: parseS3Time(d.LastModified),
			VersionID:    d.VersionID,
			IsLatest:     d.IsLatest,
			DeleteMarker: true,
		})
	}

	prefixes := len(page.CommonPrefixes)
	sort.SliceStable(out[prefixes:], func(i, j int) bool {
		a, b := out[prefixes+i], out[prefixes+j]
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.LastModified.After(b.LastModified)
	})
	return out
}
