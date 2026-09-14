package s3

import (
	"strings"
	"testing"
	"time"
)

// presignClient builds a client with fixed credentials and a frozen clock, so a
// presigned URL is reproducible and can be compared against a golden value.
func presignClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{
		Endpoint:  "https://s3.example.com",
		Region:    "garage",
		AccessKey: "GKtest",
		SecretKey: "secret",
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) }
	return c
}

// The golden signatures come from an independent SigV4 query-signing
// implementation written from the AWS specification ("Authenticating Requests:
// Using Query Parameters"), not from this package. A URL koc computes is only
// useful if a *server* accepts it, so agreeing with a second implementation is
// the property worth asserting — a test that re-derived the value with these
// same functions would pass however wrong they both were.
func TestPresignGetObjectMatchesTheSpec(t *testing.T) {
	c := presignClient(t)

	// A space in the key is the interesting case: net/url would encode it as
	// "+" in a query and leave it alone in a path, either of which breaks the
	// signature.
	got, err := c.PresignGetObject("db-backups", "dump 1.sql.gz", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://s3.example.com/db-backups/dump%201.sql.gz?" +
		"X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=GKtest%2F20260914%2Fgarage%2Fs3%2Faws4_request" +
		"&X-Amz-Date=20260914T120000Z" +
		"&X-Amz-Expires=3600" +
		"&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=8190674d81d0695616b83fa8f54b73ba0d8fa202bc6b505ce52ba8b516771afc"
	if got != want {
		t.Errorf("presigned URL\n got %s\nwant %s", got, want)
	}
}

func TestPresignPutObjectMatchesTheSpec(t *testing.T) {
	c := presignClient(t)

	got, err := c.PresignPutObject("db-backups", "incoming.bin", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const wantSig = "X-Amz-Signature=1b17213576b7451158c6054284e65dfdea8ea1c046a947af8d8993896feedf7d"
	if !strings.HasSuffix(got, wantSig) {
		t.Errorf("presigned PUT URL %s does not end with the expected signature", got)
	}
	if !strings.Contains(got, "X-Amz-Expires=900") {
		t.Errorf("presigned PUT URL %s does not carry the 15m expiry", got)
	}
}

// A version ID is an ordinary query parameter, so it has to be signed with the
// rest — in canonical (sorted) order, which puts it after the X-Amz-* names.
func TestPresignGetObjectWithVersion(t *testing.T) {
	c := presignClient(t)

	got, err := c.PresignGetObject("db-backups", "dump.sql.gz", "v2", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const wantTail = "&X-Amz-SignedHeaders=host&versionId=v2" +
		"&X-Amz-Signature=5d68330c6f95ebef61e3e373add6affbd8c3919a94a83b895c2360e6ee1630cd"
	if !strings.HasSuffix(got, wantTail) {
		t.Errorf("presigned URL %s does not end with %s", got, wantTail)
	}
}

func TestPresignRejectsBadExpiry(t *testing.T) {
	c := presignClient(t)

	for _, tc := range []struct {
		name   string
		expiry time.Duration
		want   string
	}{
		{"zero", 0, "must be positive"},
		{"negative", -time.Second, "must be positive"},
		{"past the ceiling", MaxPresignExpiry + time.Second, "at most"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.PresignGetObject("b", "k", "", tc.expiry); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Anonymous mode has no secret to sign with, so presigning must say so rather
// than emit a URL signed with an empty key.
func TestPresignRefusedWhenAnonymous(t *testing.T) {
	c, err := New(Config{Endpoint: "https://s3.example.com", Anonymous: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PresignGetObject("b", "k", "", time.Hour); err == nil ||
		!strings.Contains(err.Error(), "anonymous") {
		t.Errorf("err = %v, want it to name --s3-anonymous", err)
	}
}

func TestPresignNeedsAKey(t *testing.T) {
	if _, err := presignClient(t).PresignGetObject("b", "", "", time.Hour); err == nil {
		t.Error("presigning a bucket with no key was accepted")
	}
}
