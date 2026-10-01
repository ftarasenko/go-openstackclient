package volume

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

// An unknown -c fails every write verb before cinder sees a request: once it
// has one, an error reads as a failed write and a retry repeats it.
func TestWriteVerbs_UnknownColumnSendsNothing(t *testing.T) {
	ctx := context.Background()
	verbs := map[string]func(*gophercloud.ServiceClient, *output.Options) error{
		"volume create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runVolumeCreate(ctx, c, o, "vol", &volumeCreateFlags{size: 1}, io.Discard)
		},
		"volume extend": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runVolumeExtend(ctx, c, o, "vol", 2, io.Discard)
		},
		"volume attachment create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runAttachmentCreate(ctx, c, o, "vol", "server", &attachmentCreateFlags{}, io.Discard)
		},
		"volume attachment set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runAttachmentSet(ctx, c, o, "id", &attachmentConnectorFlags{}, io.Discard)
		},
		"volume backup create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runBackupCreate(ctx, c, o, "vol", &backupCreateFlags{}, io.Discard)
		},
		"volume backup restore": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runBackupRestore(ctx, c, o, "backup", &backupRestoreFlags{}, io.Discard)
		},
		"block storage cluster set": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runClusterSet(ctx, c, o, "cluster", &clusterSetFlags{}, io.Discard)
		},
		"volume qos create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runQoSCreate(ctx, c, o, "qos", "back-end", nil, io.Discard)
		},
		"volume snapshot create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runSnapshotCreate(ctx, c, o, "snap", &snapshotCreateFlags{}, io.Discard)
		},
		"volume transfer request create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runTransferCreate(ctx, c, o, "vol", "", false, io.Discard)
		},
		"volume transfer request accept": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runTransferAccept(ctx, c, o, "id", "key", io.Discard)
		},
		"volume type create": func(c *gophercloud.ServiceClient, o *output.Options) error {
			return runTypeCreate(ctx, c, o, "type", &typeCreateFlags{}, false, io.Discard)
		},
	}
	for name, run := range verbs {
		t.Run(name, func(t *testing.T) {
			fakeServer := th.SetupHTTP()
			defer fakeServer.Teardown()
			var hits int
			fakeServer.Mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { hits++ })

			err := run(volumeClient(fakeServer, "3.70"), &output.Options{Format: output.FormatTable, Columns: []string{"id", "bogus"}})
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
