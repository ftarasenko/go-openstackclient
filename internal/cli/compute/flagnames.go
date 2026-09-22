package compute

// Flag name and help string for --project-domain, declared once so the three
// flavor write verbs and "keypair list" stay worded identically.
const (
	flagProjectDomain = "project-domain"
	helpProjectDomain = "domain owning --project, to disambiguate the name (name or ID)"
)

// Column headers reused across this package's tables.
const (
	colUserID = "User ID"
)
