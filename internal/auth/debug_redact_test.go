package auth

import (
	"strings"
	"testing"
)

// Secrets that the exact-key list missed: names that end in a listed key, and
// a JSON-patch value whose path names one.
func TestRedact_SecretKeyTailsAndPatch(t *testing.T) {
	for name, body := range map[string]string{
		"ironic driver_info":    `{"driver_info":{"ipmi_password":"S3CR3T","redfish_password":"S3CR3T","snmp_priv_key":"S3CR3T"}}`,
		"ironic json-patch":     `[{"op":"add","path":"/driver_info/ipmi_password","value":"S3CR3T"}]`,
		"keystone password set": `{"user":{"password":"S3CR3T","original_password":"S3CR3T"}}`,
		"cinder transfer":       `{"transfer":{"id":"t1","auth_key":"S3CR3T"}}`,
		"fallback (not JSON)":   `{"user":{"original_password":"S3CR3T"} trailing`,
		"nova rescue":           `{"rescue":{"rescue_password":"S3CR3T"}}`,
	} {
		if got := redact("POST /x HTTP/1.1\r\nHost: h\r\n\r\n" + body); strings.Contains(got, "S3CR3T") {
			t.Errorf("%s leaked:\n%s", name, got)
		}
	}
	// A field that only mentions a secret stays readable, and so does a patch
	// to an ordinary path.
	got := redact("HTTP/1.1 200 OK\r\n\r\n" +
		`{"user":{"password_expires_at":"2030-01-01"},"patch":[{"op":"add","path":"/driver_info/ipmi_address","value":"192.0.2.1"}]}`)
	for _, want := range []string{"2030-01-01", "192.0.2.1"} {
		if !strings.Contains(got, want) {
			t.Errorf("over-redacted %q:\n%s", want, got)
		}
	}
}
