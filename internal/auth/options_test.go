package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

// EnvBool must fail CLOSED. The bug this replaces treated every unparseable value
// as true, so OS_INSECURE=no disabled TLS certificate verification.
func TestEnvBool_FailsClosed(t *testing.T) {
	cases := []struct {
		val      string
		want     bool
		wantBad  bool // recorded as an unusable value
		wantWarn bool
	}{
		{val: "", want: false},
		{val: "1", want: true},
		{val: "true", want: true},
		{val: "TRUE", want: true},
		{val: "yes", want: true},
		{val: "on", want: true},
		{val: "enabled", want: true},
		{val: "0", want: false},
		{val: "false", want: false},
		{val: "no", want: false},
		{val: "No", want: false},
		{val: "off", want: false},
		{val: "disabled", want: false},
		{val: " no ", want: false},
		{val: "maybe", want: false, wantBad: true, wantWarn: true},
		{val: "2", want: false, wantBad: true, wantWarn: true},
	}
	for _, tc := range cases {
		t.Run("OS_INSECURE="+tc.val, func(t *testing.T) {
			resetBadEnv()
			t.Cleanup(resetBadEnv)
			t.Setenv("OS_INSECURE", tc.val)

			var got bool
			warn := captureStderr(t, func() { got = EnvBool("OS_INSECURE") })

			if got != tc.want {
				t.Errorf("EnvBool(%q) = %v, want %v", tc.val, got, tc.want)
			}
			if bad := envError() != nil; bad != tc.wantBad {
				t.Errorf("envError() non-nil = %v, want %v", bad, tc.wantBad)
			}
			if warned := strings.Contains(warn, "WARNING"); warned != tc.wantWarn {
				t.Errorf("stderr = %q, want a warning = %v", warn, tc.wantWarn)
			}
		})
	}
}

// The whole point of the fix, at the flag level: OS_INSECURE=no must leave
// certificate verification ON.
func TestInsecureFlagDefault_OSInsecureNo(t *testing.T) {
	resetBadEnv()
	t.Cleanup(resetBadEnv)
	t.Setenv("OS_INSECURE", "no")

	o := &Options{}
	fs := pflag.NewFlagSet("koc", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if o.Insecure {
		t.Fatal("OS_INSECURE=no must not disable TLS verification")
	}
	cfg, insecure, err := o.resolveTLSConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if insecure || cfg.InsecureSkipVerify {
		t.Error("resolveTLSConfig turned OS_INSECURE=no into InsecureSkipVerify")
	}
}

// An unparseable security toggle is not silently ignored: the command that would
// use the credential fails.
func TestAuthenticate_RejectsUnusableEnvBool(t *testing.T) {
	resetBadEnv()
	t.Cleanup(resetBadEnv)
	t.Setenv("OS_INSECURE", "sure-why-not")

	o := &Options{}
	fs := pflag.NewFlagSet("koc", pflag.ContinueOnError)
	_ = captureStderr(t, func() { o.AddFlags(fs) })

	_, err := o.Authenticate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "OS_INSECURE") {
		t.Fatalf("Authenticate() = %v, want an error naming OS_INSECURE", err)
	}
}

func TestEnvDuration(t *testing.T) {
	const def = 300 * time.Second
	cases := []struct {
		val     string
		want    time.Duration
		wantBad bool
	}{
		{val: "", want: def},
		{val: "90s", want: 90 * time.Second},
		{val: "2m", want: 2 * time.Minute},
		{val: "45", want: 45 * time.Second},
		{val: "0", want: 0},
		{val: "later", want: def, wantBad: true},
		{val: "-5", want: def, wantBad: true},
	}
	for _, tc := range cases {
		t.Run("OS_TIMEOUT="+tc.val, func(t *testing.T) {
			resetBadEnv()
			t.Cleanup(resetBadEnv)
			t.Setenv("OS_TIMEOUT", tc.val)

			var got time.Duration
			_ = captureStderr(t, func() { got = envDuration("OS_TIMEOUT", def) })

			if got != tc.want {
				t.Errorf("envDuration(%q) = %v, want %v", tc.val, got, tc.want)
			}
			if bad := envError() != nil; bad != tc.wantBad {
				t.Errorf("envError() non-nil = %v, want %v", bad, tc.wantBad)
			}
		})
	}
}

// The default must be a finite timeout: a zero-value http.Client waits forever on
// an endpoint that accepts the connection and never answers.
func TestTimeoutFlagDefault(t *testing.T) {
	t.Setenv("OS_TIMEOUT", "")
	o := &Options{}
	fs := pflag.NewFlagSet("koc", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if o.Timeout != defaultHTTPTimeout {
		t.Errorf("--timeout default = %v, want %v", o.Timeout, defaultHTTPTimeout)
	}
	if err := fs.Parse([]string{"--timeout", "15s"}); err != nil {
		t.Fatal(err)
	}
	if o.Timeout != 15*time.Second {
		t.Errorf("--timeout 15s = %v", o.Timeout)
	}
}
