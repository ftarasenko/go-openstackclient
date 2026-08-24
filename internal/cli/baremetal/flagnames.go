package baremetal

// Flag names used by more than one command in this package. Each is looked up
// as well as declared (Changed / MarkFlagsMutuallyExclusive / the local
// mutuallyExclusive and enableDisable helpers), and pflag reports an unknown
// name as "not set" rather than erroring — so a typo in a lookup copy silently
// disables the check it guards instead of failing loudly.
const (
	flagResourceClass    = "resource-class"
	flagConductorGroup   = "conductor-group"
	flagDriverInfo       = "driver-info"
	flagInstanceUUID     = "instance-uuid"
	flagAutomatedClean   = "automated-clean"
	flagPXEEnabled       = "pxe-enabled"
	flagNoAutomatedClean = "no-automated-clean"
)

// Repeated cobra Use lines.
const (
	useListNode = "list <node>"
)

// Other literals repeated across this package.
const (
	// flagInterfaceSuffix is appended to a hardware-interface name to form the
	// flag ("boot" -> --boot-interface); it is not a flag name on its own.
	flagInterfaceSuffix = "-interface"

	pathAutomatedClean = "/automated_clean"
	releaseBobcat      = "OpenStack 2023.2"
)
