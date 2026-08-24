package network

// Flag names used by more than one command in this package. Each is looked up
// as well as declared (Changed / MarkFlagsMutuallyExclusive / the local
// mutuallyExclusive and enableDisable helpers), and pflag reports an unknown
// name as "not set" rather than erroring — so a typo in a lookup copy silently
// disables the check it guards instead of failing loudly.
const (
	flagNoShare             = "no-share"
	flagNoDefault           = "no-default"
	flagNoDHCP              = "no-dhcp"
	flagTargetProject       = "target-project"
	flagNetworkType         = "network-type"
	flagDeviceOwner         = "device-owner"
	flagMACAddress          = "mac-address"
	flagSecurityGroup       = "security-group"
	flagNoSecurityGroup     = "no-security-group"
	flagFixedIP             = "fixed-ip"
	flagAllowedAddress      = "allowed-address"
	flagEnablePortSecurity  = "enable-port-security"
	flagDisablePortSecurity = "disable-port-security"
	flagEnableSNAT          = "enable-snat"
	flagDisableSNAT         = "disable-snat"
	flagDNSNameserver       = "dns-nameserver"
	flagAddressScope        = "address-scope"
	flagShare               = "share"
	flagDefault             = "default"
	flagDHCP                = "dhcp"
	flagTargetAllProjects   = "target-all-projects"
	flagNoAllowedAddress    = "no-allowed-address"
)

// Other literals repeated across this package.
const (
	fieldProviderNetworkType     = "provider:network_type"
	fieldProviderPhysicalNetwork = "provider:physical_network"
)
