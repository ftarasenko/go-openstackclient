package auth

import "strings"

// applyOpenrcVars maps openrc OS_* variables onto o. A field is only set when its
// flag was not explicitly provided, so explicit CLI flags override Vault values.
func (o *Options) applyOpenrcVars(kv map[string]string) {
	set := func(flag string, dst *string, keys ...string) {
		if o.fs != nil && o.fs.Changed(flag) {
			return
		}
		for _, k := range keys {
			if v := kv[k]; v != "" {
				*dst = v
				// pflag never saw this value, but it counts as explicitly
				// given (see Options.explicitlySet).
				o.markForced(flag)
				return
			}
		}
	}
	set("os-auth-url", &o.AuthURL, "OS_AUTH_URL")
	set("os-username", &o.Username, "OS_USERNAME")
	set("os-user-id", &o.UserID, "OS_USER_ID")
	set(flagOSPassword, &o.Password, "OS_PASSWORD")
	set(flagOSProjectName, &o.ProjectName, "OS_PROJECT_NAME", "OS_TENANT_NAME")
	set(flagOSProjectID, &o.ProjectID, "OS_PROJECT_ID", "OS_TENANT_ID")
	set("os-project-domain-name", &o.ProjectDomainName, "OS_PROJECT_DOMAIN_NAME")
	set("os-user-domain-name", &o.UserDomainName, "OS_USER_DOMAIN_NAME")
	set("os-domain-name", &o.DomainName, "OS_DOMAIN_NAME")
	set("os-project-domain-id", &o.ProjectDomainID, "OS_PROJECT_DOMAIN_ID")
	set("os-user-domain-id", &o.UserDomainID, "OS_USER_DOMAIN_ID")
	set("os-domain-id", &o.DomainID, "OS_DOMAIN_ID")
	set("os-region-name", &o.RegionName, "OS_REGION_NAME")
	set("os-interface", &o.Interface, "OS_INTERFACE", "OS_ENDPOINT_TYPE")
	set("os-cacert", &o.CACert, "OS_CACERT")
	set("os-cert", &o.ClientCert, "OS_CERT")
	set("os-key", &o.ClientKey, "OS_KEY")
	set("os-application-credential-id", &o.AppCredID, "OS_APPLICATION_CREDENTIAL_ID")
	set("os-application-credential-name", &o.AppCredName, "OS_APPLICATION_CREDENTIAL_NAME")
	set("os-application-credential-secret", &o.AppCredSecret, "OS_APPLICATION_CREDENTIAL_SECRET")
	set(flagOSAuthType, &o.AuthType, "OS_AUTH_TYPE")
	set(flagOSToken, &o.Token, "OS_TOKEN")
	set("os-trust-id", &o.TrustID, "OS_TRUST_ID")
	set("os-endpoint", &o.Endpoint, "OS_ENDPOINT")
	set("os-system-scope", &o.SystemScope, "OS_SYSTEM_SCOPE")

	// OS_ENDPOINT_TYPE is sometimes "publicURL"; normalize to the interface name.
	if o.fs == nil || !o.fs.Changed("os-interface") {
		o.Interface = strings.TrimSuffix(o.Interface, "URL")
	}
}
