package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4 for the S3 service, computed with the standard
// library alone. It is ~100 lines because that is all the algorithm is: a
// canonical rendering of the request, hashed, then HMAC-SHA256'd four times
// down a date/region/service/terminator chain. Vendoring an SDK for it would
// cost the offline build a dozen modules and the binary several megabytes for
// no capability koc needs.
const (
	algorithm = "AWS4-HMAC-SHA256"
	service   = "s3"
	terminacc = "aws4_request"

	// emptySHA256 is the payload hash of a zero-length body — every GET, HEAD
	// and list request koc sends.
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	amzDateFormat   = "20060102T150405Z"
	dateStampFormat = "20060102"
)

// signableHeaders are the non-x-amz headers included in the signature. Every
// x-amz-* header present is signed as well (S3 requires it), so this list only
// has to name the standard ones that carry request semantics. It deliberately
// excludes headers Go fills in after signing — Content-Length, User-Agent,
// Accept-Encoding — which would make the signature disagree with the wire.
var signableHeaders = map[string]bool{
	"host":         true,
	"content-type": true,
	"content-md5":  true,
	"date":         true,
	"range":        true,
}

// sign adds the SigV4 Authorization header (plus the x-amz-date and
// x-amz-content-sha256 headers it covers) to req. payloadHash is the hex
// SHA-256 of the request body, emptySHA256 for a bodiless request.
func (c *Client) sign(req *http.Request, payloadHash string, now time.Time) {
	utc := now.UTC()
	amzDate, dateStamp := utc.Format(amzDateFormat), utc.Format(dateStampFormat)

	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signed, canonicalHeaders := canonicalHeaders(req)
	signedList := strings.Join(signed, ";")

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		req.URL.RawQuery, // built by canonicalQuery, already in canonical form
		canonicalHeaders,
		signedList,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, c.cfg.Region, service, terminacc}, "/")
	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	sig := hex.EncodeToString(hmacSHA256(signingKey(c.cfg.SecretKey, dateStamp, c.cfg.Region), stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, c.cfg.AccessKey, scope, signedList, sig))
}

// signingKey derives the request-scoped key: the secret is never used directly,
// only as the seed of a date→region→service→terminator HMAC chain.
func signingKey(secret, dateStamp, region string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	return hmacSHA256(k, terminacc)
}

// canonicalHeaders renders the signed headers block and returns the sorted list
// of names that went into it.
func canonicalHeaders(req *http.Request) (names []string, block string) {
	vals := map[string]string{"host": hostHeader(req)}
	for name, v := range req.Header {
		lower := strings.ToLower(name)
		if !signableHeaders[lower] && !strings.HasPrefix(lower, "x-amz-") {
			continue
		}
		trimmed := make([]string, len(v))
		for i, one := range v {
			trimmed[i] = collapseSpaces(one)
		}
		vals[lower] = strings.Join(trimmed, ",")
	}

	names = make([]string, 0, len(vals))
	for k := range vals {
		names = append(names, k)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, k := range names {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(vals[k])
		b.WriteByte('\n')
	}
	return names, b.String()
}

// hostHeader returns the value the Host header will carry on the wire. Go keeps
// it out of Request.Header, deriving it from Request.Host or the URL, so the
// signature has to do the same.
func hostHeader(req *http.Request) string {
	if req.Host != "" {
		return req.Host
	}
	return req.URL.Host
}

// canonicalURI is the URI-encoded path with "/" preserved. RawPath is set by the
// URL builders in this package to exactly this encoding, so what is signed is
// what is sent — which matters for a key containing "+", a space or any other
// character Go's own path escaping would leave alone.
func canonicalURI(u *url.URL) string {
	if u.Path == "" {
		return "/"
	}
	return uriEncode(u.Path, true)
}

// canonicalQuery renders query parameters in the order and encoding SigV4
// requires: sorted by name then value, every character percent-encoded except
// the RFC 3986 unreserved set. The result is used verbatim as URL.RawQuery so
// the signed string and the request line cannot drift apart.
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, uriEncode(k, false)+"="+uriEncode(v, false))
		}
	}
	return strings.Join(parts, "&")
}

// uriEncode percent-encodes per RFC 3986, keeping only the unreserved set (and
// "/" when keepSlash). net/url cannot be used here: it encodes a space as "+"
// in queries and leaves "+" itself alone in paths, both of which break the
// signature.
func uriEncode(s string, keepSlash bool) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		case ch == '/' && keepSlash:
			b.WriteByte('/')
		default:
			b.WriteByte('%')
			b.WriteByte(upperhex[ch>>4])
			b.WriteByte(upperhex[ch&0xf])
		}
	}
	return b.String()
}

// collapseSpaces trims a header value and collapses runs of spaces to one, as
// the canonical-headers rules require.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
