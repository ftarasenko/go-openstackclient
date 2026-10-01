package dns

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	th "github.com/gophercloud/gophercloud/v2/testhelper"

	"github.com/ftarasenko/go-openstackclient/internal/output"
)

// An unknown -c fails every write verb before designate sees a request: once it
// has one, an error reads as a failed write and a retry repeats it.
func TestWriteVerbs_UnknownColumnSendsNothing(t *testing.T) {
	ctx := context.Background()
	verbs := map[string]func(*gophercloud.ServiceClient, *output.Options) error{
		"zone create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runZoneCreate(ctx, c, o, "example.com.", &zoneCreateFlags{}, io.Discard)
		},
		"zone set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runZoneSet(ctx, c, o, "example.com.", &zoneSetFlags{}, io.Discard)
		},
		"recordset create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runRecordSetCreate(ctx, c, o, "example.com.", "www", &recordSetCreateFlags{}, io.Discard)
		},
		"recordset set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runRecordSetSet(ctx, c, o, "example.com.", "www", &recordSetSetFlags{}, io.Discard)
		},
		"zone blacklist create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runZoneBlacklistCreate(ctx, c, o, "^x$", "", &commonOptions{}, io.Discard)
		},
		"zone blacklist set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runZoneBlacklistSet(ctx, c, o, "id", &blacklistSetFlags{}, io.Discard)
		},
		"tld create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runTLDCreate(ctx, c, o, "com", "", &commonOptions{}, io.Discard)
		},
		"tld set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runTLDSet(ctx, c, o, "com", &tldSetFlags{}, io.Discard)
		},
		"zone export create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runZoneExportCreate(ctx, c, o, "example.com.", &commonOptions{}, io.Discard)
		},
		"zone import create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runZoneImportCreate(ctx, c, o, "zonefile", nil, &commonOptions{}, io.Discard)
		},
		"ptr record set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runPTRRecordSet(ctx, c, o, "RegionOne:id", "ptr.example.com.", &ptrRecordSetFlags{}, io.Discard)
		},
		"dns quota set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runDNSQuotaSet(ctx, c, o, "project", &dnsQuotaSetFlags{}, io.Discard)
		},
		"tsigkey create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runTSIGKeyCreate(ctx, c, o, "key", &tsigKeyWriteFlags{}, io.Discard)
		},
		"tsigkey set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runTSIGKeySet(ctx, c, o, "key", &tsigKeyWriteFlags{}, io.Discard)
		},
		"zone transfer request create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runZoneTransferRequestCreate(ctx, c, o, "example.com.", "", "", io.Discard)
		},
		"zone transfer request set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runZoneTransferRequestSet(ctx, c, o, "id", "", "", io.Discard)
		},
		"zone transfer accept request": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runZoneTransferAcceptRequest(ctx, c, o, "id", "key", io.Discard)
		},
		"zone share create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runZoneShareCreate(ctx, c, o, "example.com.", "project", io.Discard)
		},
	}
	for name, run := range verbs {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			var hits int
			fakeServer.Mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { hits++ })

			err := run(dnsClient(fakeServer), &output.Options{Format: output.FormatTable, Columns: []string{"id", "bogus"}})
			var ce *output.ColumnError
			if !errors.As(err, &ce) || ce.Rendering {
				t.Fatalf("err = %v, want a pre-flight ColumnError", err)
			}
			if hits != 0 {
				t.Errorf("%d request(s) sent despite the bad column", hits)
			}
		})
	}
}

// The zone catalog includes the field rendered only where designate reports it.
func TestZoneColumns_IncludeShared(t *testing.T) {
	if err := (&output.Options{Columns: []string{"shared"}}).CheckColumns(zoneColumns...); err != nil {
		t.Errorf("zone catalog rejects shared: %v", err)
	}
}
