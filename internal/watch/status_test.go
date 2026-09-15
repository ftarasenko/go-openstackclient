package watch

import (
	"testing"
	"time"
)

func TestCompactDuration(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0ms"},
		{-time.Second, "0ms"},
		{218394122 * time.Nanosecond, "218ms"},
		{999600 * time.Microsecond, "1s"},
		{time.Second, "1s"},
		{1500 * time.Millisecond, "1.5s"},
		{90 * time.Second, "1m30s"},
	} {
		if got := compactDuration(tc.in); got != tc.want {
			t.Errorf("compactDuration(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestErrSummaryFitsOnOneLine(t *testing.T) {
	// gophercloud prints the whole response body in its error, which is exactly
	// what must not reach the status line.
	got := errSummary(httpErr(503))
	if got != "503 Service Unavailable" {
		t.Errorf("errSummary = %q", got)
	}

	long := make([]byte, 0, 200)
	for range 200 {
		long = append(long, 'x')
	}
	if n := len([]rune(errSummary(plainError(string(long))))); n > 80 {
		t.Errorf("errSummary returned %d runes, want at most 80", n)
	}
}

type plainError string

func (e plainError) Error() string { return string(e) }

func TestPlural(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{{0, "0 rows"}, {1, "1 row"}, {42, "42 rows"}} {
		if got := plural(tc.n, "row"); got != tc.want {
			t.Errorf("plural(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
