package s3

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// TestSignGetObjectVector checks the signer against AWS's published worked
// example for a GET Object request ("Signature Calculations for the
// Authorization Header: Transferring Payload in a Single Chunk"). It is the one
// test that proves the whole chain — canonical request, string to sign, the
// four-step signing key — rather than any single step, so the credentials and
// the expected signature below are AWS's documentation values verbatim and must
// not be "tidied".
func TestSignGetObjectVector(t *testing.T) {
	c := &Client{cfg: Config{
		Region:    "us-east-1",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-9")

	c.sign(req, emptySHA256, time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))

	const want = "AWS4-HMAC-SHA256 " +
		"Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization =\n%s\nwant\n%s", got, want)
	}
	if got := req.Header.Get("X-Amz-Date"); got != "20130524T000000Z" {
		t.Errorf("X-Amz-Date = %q", got)
	}
}

// TestSignPutObjectVector is AWS's second worked example from the same page: a
// PUT with a body hash and a signed non-x-amz header (Date), which the GET
// vector does not exercise.
func TestSignPutObjectVector(t *testing.T) {
	c := &Client{cfg: Config{
		Region:    "us-east-1",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, "https://examplebucket.s3.amazonaws.com/test%24file.text", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Date", "Fri, 24 May 2013 00:00:00 GMT")
	req.Header.Set("X-Amz-Storage-Class", "REDUCED_REDUNDANCY")

	const bodyHash = "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072"
	c.sign(req, bodyHash, time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))

	const want = "AWS4-HMAC-SHA256 " +
		"Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class, " +
		"Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization =\n%s\nwant\n%s", got, want)
	}
}

func TestURIEncode(t *testing.T) {
	tests := []struct {
		in        string
		keepSlash bool
		want      string
	}{
		{"/db-backups/e2e-mariadb.tar.gz", true, "/db-backups/e2e-mariadb.tar.gz"},
		{"/a b/c+d", true, "/a%20b/c%2Bd"},
		{"/a/b", false, "%2Fa%2Fb"},
		{"~-._", true, "~-._"},
		{"ключ", true, "%D0%BA%D0%BB%D1%8E%D1%87"},
	}
	for _, tt := range tests {
		if got := uriEncode(tt.in, tt.keepSlash); got != tt.want {
			t.Errorf("uriEncode(%q, %v) = %q, want %q", tt.in, tt.keepSlash, got, tt.want)
		}
	}
}

// TestCanonicalQuery pins the two properties SigV4 needs and net/url does not
// provide: sorting by name, and a space encoded as %20 rather than "+".
func TestCanonicalQuery(t *testing.T) {
	q := url.Values{
		"prefix":    {"a b"},
		"list-type": {"2"},
		"max-keys":  {"1000"},
	}
	const want = "list-type=2&max-keys=1000&prefix=a%20b"
	if got := canonicalQuery(q); got != want {
		t.Errorf("canonicalQuery = %q, want %q", got, want)
	}
}

// TestCanonicalURIMatchesRequestLine guards the invariant that makes the
// signature verifiable: what url() puts on the wire is byte-for-byte what
// canonicalURI signs.
func TestCanonicalURIMatchesRequestLine(t *testing.T) {
	c, err := New(Config{Endpoint: "https://s3.example.com", AccessKey: "a", SecretKey: "b", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	u := c.url("db-backups", "dir/a b+c.tar.gz", nil)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := req.URL.EscapedPath(), canonicalURI(u); got != want {
		t.Errorf("request path = %q, signed path = %q", got, want)
	}
	if want := "/db-backups/dir/a%20b%2Bc.tar.gz"; canonicalURI(u) != want {
		t.Errorf("canonicalURI = %q, want %q", canonicalURI(u), want)
	}
}
