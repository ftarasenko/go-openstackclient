package identity

// Flag names used by more than one command in this package. Each is looked up
// as well as declared (Changed / MarkFlagsMutuallyExclusive / the local
// mutuallyExclusive and enableDisable helpers), and pflag reports an unknown
// name as "not set" rather than erroring — so a typo in a lookup copy silently
// disables the check it guards instead of failing loudly.
const (
	flagUserDomain  = "user-domain"
	flagImpliedRole = "implied-role"
)

// Column headers reused across this package's tables.
const (
	colDomainID  = "Domain ID"
	colProjectID = "Project ID"
	colExpiresAt = "Expires At"
)

// Flag help strings reused across commands, so the wording stays identical.
const (
	helpDomainProject = "domain owning the project (name or ID)"
	helpDomainUser    = "domain owning the user (name or ID)"
	helpDomainRole    = "domain the role belongs to (name or ID)"
	helpOwningUser    = "owning user (name or ID; defaults to the current user)"
)
