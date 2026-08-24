package loadbalancer

// Flag names used by more than one command in this package. Each is looked up
// as well as declared (Changed / MarkFlagsMutuallyExclusive / the local
// mutuallyExclusive and enableDisable helpers), and pflag reports an unknown
// name as "not set" rather than erroring — so a typo in a lookup copy silently
// disables the check it guards instead of failing loudly.
const (
	flagProjectDomain  = "project-domain"
	flagCompareType    = "compare-type"
	flagNoInvert       = "no-invert"
	flagEnableBackup   = "enable-backup"
	flagDisableBackup  = "disable-backup"
	flagEnableTLS      = "enable-tls"
	flagDisableTLS     = "disable-tls"
	flagLBAlgorithm    = "lb-algorithm"
	flagFlavorData     = "flavor-data"
	flagVIPQoSPolicyID = "vip-qos-policy-id"
	flagWaitTimeout    = "wait-timeout"
	flagInvert         = "invert"
)

// Flag help strings reused across commands, so the wording stays identical.
const (
	helpProjectDomain = "domain owning the project (name or ID)"
	helpWaitTimeout   = "maximum time to wait for --wait to complete"
)

// Repeated cobra Use lines.
const (
	useShowLoadBalancer = "show <load-balancer>"
)
