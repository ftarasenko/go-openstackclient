package server

// Flag names used by more than one command in this package. Each is looked up
// as well as declared (Changed / MarkFlagsMutuallyExclusive / the local
// mutuallyExclusive and enableDisable helpers), and pflag reports an unknown
// name as "not set" rather than erroring — so a typo in a lookup copy silently
// disables the check it guards instead of failing loudly.
const (
	flagRootPassword = "root-password"
	flagWaitTimeout  = "wait-timeout"
	flagPassword     = "password"
	flagNoPassword   = "no-password"
)

// Flag help strings reused across commands, so the wording stays identical.
const (
	helpWaitTimeout  = "maximum time to wait for --wait to complete"
	helpInterfaceTag = "tag for the attached interface (nova 2.49 or later)"
)

// Other literals repeated across this package.
const (
	bootedFromVolume = "N/A (booted from volume)"
)
