package dns

import (
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/quotas"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/transfer/accept"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/transfer/request"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/tsigkeys"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/zones"

	dnsapi "github.com/ftarasenko/go-openstackclient/pkg/openstack/dns"
)

// Every column each resource's write verbs can render, taken from the field
// builders so they cannot drift. A write verb checks -c against its catalog
// before sending anything: once designate has the request, an error reads as a
// failed write and a retry repeats it.
var (
	blacklistColumns       = columnsOf(blacklistFields(&dnsapi.Blacklist{}))
	tldColumns             = columnsOf(tldFields(&dnsapi.TLD{}))
	zoneExportColumns      = columnsOf(zoneExportFields(&dnsapi.ZoneExport{}))
	zoneImportColumns      = columnsOf(zoneImportFields(&dnsapi.ZoneImport{}))
	ptrRecordColumns       = columnsOf(ptrRecordFields(&dnsapi.PTRRecord{}))
	dnsQuotaColumns        = columnsOf(dnsQuotaFields(&quotas.Quota{}))
	tsigKeyColumns         = columnsOf(tsigKeyFields(&tsigkeys.TSIGKey{}))
	recordSetColumns       = columnsOf(recordSetShowFields(&dnsapi.RecordSet{}))
	transferRequestColumns = columnsOf(transferRequestFields(&request.TransferRequest{}))
	transferAcceptColumns  = columnsOf(transferAcceptFields(&accept.TransferAccept{}))
	zoneShareColumns       = columnsOf(zoneShareFields(&zones.ZoneShare{}))
	// shared is rendered only where designate reports it.
	zoneColumns = columnsOf(zoneShowFields(&dnsapi.ZoneDetail{Zone: &zones.Zone{}, Shared: new(bool)}))
)

func columnsOf(fields []string, _ []any) []string { return fields }
