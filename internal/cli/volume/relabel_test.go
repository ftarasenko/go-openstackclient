package volume

import (
	"context"
	"errors"
	"fmt"
	"testing"

	volumeapi "github.com/ftarasenko/koc/pkg/openstack/volume"
)

func TestRelabel(t *testing.T) {
	base := errors.New("Resource not found")
	if relabel(nil, "x %q", "a") != nil || inner(nil) != nil {
		t.Fatal("nil did not stay nil")
	}
	wrapped := fmt.Errorf("getting volume 0b6c: %w", base)
	if got := relabel(wrapped, "getting volume %q", "data").Error(); got != `getting volume "data": Resource not found` {
		t.Errorf("relabel = %s", got)
	}
	if got := relabel(base, "getting volume %q", "data"); !errors.Is(got, base) || got.Error() != base.Error() {
		t.Errorf("an unwrapped error changed: %v", got)
	}
	if got := inner(wrapped); got.Error() != base.Error() || inner(base).Error() != base.Error() {
		t.Error("inner did not strip exactly one wrap")
	}
}

func TestRelabelWait(t *testing.T) {
	failed := func(*volumeapi.WaitFailedError) error { return errors.New("settled badly") }
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("waiting for volume v1: %w", context.DeadlineExceeded), `waiting for volume "data": context deadline exceeded`},
		{fmt.Errorf("waiting for volume v1: %w", context.Canceled), `waiting for volume "data": context canceled`},
		{fmt.Errorf("polling volume v1: %w", errors.New("boom")), `polling volume "data": boom`},
		{&volumeapi.WaitFailedError{VolumeID: "v1", Reason: volumeapi.ErrorStatus}, "settled badly"},
	} {
		if got := relabelWait(tc.err, "data", failed); got == nil || got.Error() != tc.want {
			t.Errorf("relabelWait(%v) = %v; want %s", tc.err, got, tc.want)
		}
	}
	if relabelWait(nil, "data", failed) != nil {
		t.Error("nil did not stay nil")
	}
}
