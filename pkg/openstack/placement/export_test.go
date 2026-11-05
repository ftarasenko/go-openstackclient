package placement

// The microversion helpers, the allocation filter and the usage fold, exposed
// to the black-box tests.
var (
	ParseMicroversion     = parseMicroversion
	CompareMicroversions  = compareMicroversions
	IsNumericMicroversion = isNumericMicroversion
	PlacementMaxVersion   = placementMaxVersion
	ConcreteClient        = concreteClient
	KeepAllocations       = keepAllocations
	UsageByClass          = usageByClass
)
