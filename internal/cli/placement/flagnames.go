package placement

// Flag names used by more than one command in this package. Each is looked up
// as well as declared (Changed / MarkFlagsMutuallyExclusive / the local
// mutuallyExclusive and enableDisable helpers), and pflag reports an unknown
// name as "not set" rather than erroring — so a typo in a lookup copy silently
// disables the check it guards instead of failing loudly.
const (
	flagParentProvider = "parent-provider"
)

// Column headers reused across this package's tables.
const (
	colResourceClass = "Resource Class"
)

// Repeated cobra Use lines.
const (
	useSetUUID = "set <uuid>"
)
