package volume

import (
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/attachments"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/backups"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/qos"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/transfers"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumetypes"
)

// Every column each resource's write verbs can render, taken from the field
// builders so they cannot drift. A write verb checks -c against its catalog
// before sending anything: once cinder has the request, an error reads as a
// failed write and a retry repeats it.
var (
	attachmentColumns = columnsOf(attachmentShowFields(&attachments.Attachment{}))
	backupColumns     = columnsOf(backupShowFields(&backups.Backup{}))
	qosColumns        = columnsOf(qosShowFields(&qos.QoS{}))
	snapshotColumns   = columnsOf(snapshotShowFields(&snapshots.Snapshot{}))
	transferColumns   = columnsOf(transferShowFields(&transfers.Transfer{}))
	volumeColumns     = columnsOf(volumeShowFields(&volumes.Volume{}))
	volumeTypeColumns = columnsOf(typeShowFields(&volumetypes.VolumeType{}))

	backupRestoreColumns = []string{"backup_id", "volume_id", "volume_name"}
)

func columnsOf(fields []string, _ []any) []string { return fields }
