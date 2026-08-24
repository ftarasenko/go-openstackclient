package quota

// Flag names used by more than one command in this package. Each is looked up
// as well as declared (Changed / MarkFlagsMutuallyExclusive / the local
// mutuallyExclusive and enableDisable helpers), and pflag reports an unknown
// name as "not set" rather than erroring — so a typo in a lookup copy silently
// disables the check it guards instead of failing loudly.
const (
	flagKeyPairs           = "key-pairs"
	flagServerGroups       = "server-groups"
	flagServerGroupMembers = "server-group-members"
	flagPerVolumeGigabytes = "per-volume-gigabytes"
	flagBackupGigabytes    = "backup-gigabytes"
	flagVolumeGroups       = "volume-groups"
	flagFloatingIPs        = "floating-ips"
	flagSecgroupRules      = "secgroup-rules"
	flagRBACPolicies       = "rbac-policies"
	flagCores              = "cores"
	flagInstances          = "instances"
	flagRAM                = "ram"
	flagProperties         = "properties"
	flagVolumes            = "volumes"
	flagSnapshots          = "snapshots"
	flagGigabytes          = "gigabytes"
	flagBackups            = "backups"
	flagNetworks           = "networks"
	flagSubnets            = "subnets"
	flagSubnetPools        = "subnetpools"
	flagPorts              = "ports"
	flagRouters            = "routers"
	flagSecgroups          = "secgroups"
	flagTrunks             = "trunks"
)
