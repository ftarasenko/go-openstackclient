package extract

import (
	"errors"
	"testing"
)

type payload struct{ Name string }

func TestOne(t *testing.T) {
	boom := errors.New("boom")
	value := &payload{Name: "x"}

	tests := []struct {
		name    string
		in      *payload
		inErr   error
		want    *payload
		wantErr error
	}{
		{name: "a value passes through", in: value, want: value},
		{name: "an error passes through", inErr: boom, wantErr: boom},
		{name: "an error wins over a nil value", inErr: boom, wantErr: boom},
		// The case the package exists for: gophercloud reports success and
		// hands back nothing, which used to reach the caller as a panic.
		{name: "success with no object becomes an error", wantErr: errNoObject},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := One(tt.in, tt.inErr)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("value = %v, want %v", got, tt.want)
			}
		})
	}
}
