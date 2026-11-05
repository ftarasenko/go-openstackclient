package baremetal

// Unexported helpers, exposed to the black-box tests.
var (
	ClassifyProvisionState = classifyProvisionState
	ExtractBIOSSetting     = extractBIOSSetting
	ExtractAllocations     = extractAllocations
)
