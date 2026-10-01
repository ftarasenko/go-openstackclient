package quota

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	volumequotas "github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/quotasets"
	computequotas "github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
	networkquotas "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/quotas"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// preflightMock counts every request and answers none, so a test can assert
// that a rejected -c sent nothing.
func preflightMock(t *testing.T) (th.FakeServer, *int) {
	t.Helper()
	fakeServer := th.SetupHTTP()
	t.Cleanup(fakeServer.Teardown)
	var calls int
	fakeServer.Mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	})
	return fakeServer, &calls
}

// badColumn is a -c selection no command renders.
func badColumn() *output.Options {
	return &output.Options{Format: output.FormatValue, Columns: []string{"bogus"}}
}

// assertPreflight checks that err is a -c rejection raised before any request.
func assertPreflight(t *testing.T, verb string, err error, calls int) {
	t.Helper()
	var ce *output.ColumnError
	if !errors.As(err, &ce) || ce.Rendering {
		t.Errorf("%s: err = %v, want a pre-flight *output.ColumnError", verb, err)
	}
	if calls != 0 {
		t.Errorf("%s: %d request(s) sent despite the bad column, want none", verb, calls)
	}
}

func TestQuotaSetChecksColumnsFirst(t *testing.T) {
	fs, calls := preflightMock(t)
	f, fl := setFlagSet(t, []string{"--cores=64"})
	f.fl, f.given = fl, f.givenBy(fl)
	err := runQuotaSet(context.Background(), oneServerSession(fs), badColumn(), "p1", f, io.Discard)
	assertPreflight(t, "quota set", err, *calls)
}

// The pre-flight accepts every column quota set can render for the services
// given, a per-volume-type quota included, and nothing for a service not given.
func TestCheckQuotaSetColumns(t *testing.T) {
	all := serviceSelection{compute: true, volume: true, network: true}
	var cols []string
	cf, _ := computeQuotaFields(&computequotas.QuotaSet{Cores: 1})
	vf, _ := volumeQuotaFields(&volumequotas.QuotaSet{Volumes: 1}, []typedQuota{{key: "gigabytes_SSD", value: 1}})
	nf, _ := networkQuotaFields(&networkquotas.Quota{Network: 1})
	cols = append(append(append(cols, cf...), vf...), nf...)
	for _, c := range cols {
		if err := checkQuotaSetColumns(&output.Options{Columns: []string{c}}, all); err != nil {
			t.Errorf("-c %s refused: %v", c, err)
		}
	}
	if err := checkQuotaSetColumns(&output.Options{Columns: []string{"gigabytes_SSD"}},
		serviceSelection{compute: true}); err == nil {
		t.Error("-c gigabytes_SSD accepted for a compute-only quota set, which never renders it")
	}
}
