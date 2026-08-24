package dns

// Flag names used by more than one command in this package. Each is looked up
// as well as declared (Changed / MarkFlagsMutuallyExclusive / the local
// mutuallyExclusive and enableDisable helpers), and pflag reports an unknown
// name as "not set" rather than erroring — so a typo in a lookup copy silently
// disables the check it guards instead of failing loudly.
const (
	flagNoDescription = "no-description"
	flagResourceID    = "resource-id"
	flagTargetProject = "target-project"
	flagDescription   = "description"
)
