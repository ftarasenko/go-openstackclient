package compute

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"
	fakeclient "github.com/gophercloud/gophercloud/v2/testhelper/client"
)

func TestRelabel(t *testing.T) {
	inner := gophercloud.ErrUnexpectedResponseCode{Actual: http.StatusNotFound}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil stays nil", nil, ""},
		{"the last quoted ID becomes the reference",
			fmt.Errorf(`granting project "p1" access to flavor "p1": %w`, inner),
			`granting project "p1" access to flavor "m1.small": ` + inner.Error()},
		{"an error without the ID is left as it is",
			fmt.Errorf(`listing flavors: %w`, inner), `listing flavors: ` + inner.Error()},
		{"an unwrapped error is left as it is", errors.New(`showing flavor "p1"`), `showing flavor "p1"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := relabel(c.err, "p1", "m1.small")
			if c.err == nil {
				if got != nil {
					t.Fatalf("got %v", got)
				}
				return
			}
			if got.Error() != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
			if c.err != nil && errors.Unwrap(c.err) != nil && !gophercloud.ResponseCodeIs(got, http.StatusNotFound) {
				t.Error("the inner error must stay reachable")
			}
		})
	}
	if err := fmt.Errorf(`showing flavor "x": %w`, inner); relabel(err, "x", "x") != err {
		t.Error("an ID that is the reference needs no rewrap")
	}
}

func TestResolveFlavor_Wording(t *testing.T) {
	fakeServer := th.SetupHTTP()
	defer fakeServer.Teardown()
	fakeServer.Mux.HandleFunc("/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"flavors":[{"id":"a","name":"dup"},{"id":"b","name":"dup"}]}`))
	})
	client := fakeclient.ServiceClient(fakeServer)
	for ref, want := range map[string]string{
		"dup":  `multiple flavors match name "dup"; specify the ID instead`,
		"none": `no flavor found with name or ID "none"`,
	} {
		if _, err := resolveFlavor(t.Context(), client, ref); err == nil || err.Error() != want {
			t.Errorf("resolveFlavor(%q) = %v, want %s", ref, err, want)
		}
	}
}
