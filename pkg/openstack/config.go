package openstack

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/config"
	"github.com/gophercloud/gophercloud/v2/openstack/config/clouds"
)

// Config is what [New] authenticates with. Exactly one of AuthOptions and
// Cloud is set.
type Config struct {
	// AuthOptions authenticates with explicit credentials. They are used as
	// given — AllowReauth included.
	AuthOptions *gophercloud.AuthOptions

	// Cloud names an entry of CloudsYAML (the content of a clouds.yaml), with
	// SecureYAML (a secure.yaml) optionally merged over it. New reads no file
	// itself: finding and reading clouds.yaml is the caller's choice.
	//
	// gophercloud's parser falls back to OS_CACERT, OS_CERT and OS_KEY for a
	// cloud entry that names no TLS files; set the files in the entry, or pass
	// TLSConfig, to keep the environment out of it.
	Cloud      string
	CloudsYAML []byte
	SecureYAML []byte

	// Region and Interface (public, internal or admin) select catalog
	// endpoints. Empty means the cloud entry's setting, and otherwise
	// gophercloud's default: the catalog's first region, and public.
	Region    string
	Interface string

	// TLSConfig is the TLS configuration of the default HTTP client. It
	// replaces whatever TLS settings the cloud entry carries.
	TLSConfig *tls.Config

	// HTTPClient, when set, carries every request — the token request
	// included — so a caller can add its own transport (tracing, metrics,
	// timeouts). TLS is then its transport's business: it is an error to set
	// TLSConfig too, and the cloud entry's TLS settings are not applied.
	HTTPClient *http.Client

	// Options apply to the service clients the Session builds.
	Options
}

// New authenticates against Keystone as cfg describes and returns the Session.
// It reads no environment variable (other than the TLS fallback noted on
// [Config]), no file and no terminal.
func New(ctx context.Context, cfg Config) (*Session, error) {
	ao, eo, cloudTLS, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	var pc *gophercloud.ProviderClient
	switch tlsCfg := cmp.Or(cfg.TLSConfig, cloudTLS); {
	case cfg.HTTPClient != nil && cfg.TLSConfig != nil:
		return nil, errors.New("openstack.Config: set TLSConfig or HTTPClient, not both")
	case cfg.HTTPClient != nil:
		pc, err = config.NewProviderClient(ctx, ao, config.WithHTTPClient(*cfg.HTTPClient))
	case tlsCfg != nil:
		pc, err = config.NewProviderClient(ctx, ao, config.WithTLSConfig(tlsCfg))
	default:
		pc, err = config.NewProviderClient(ctx, ao)
	}
	if err != nil {
		return nil, fmt.Errorf("authenticating to OpenStack: %w", err)
	}
	return FromProvider(pc, eo, cfg.Options), nil
}

// resolve produces the AuthOptions and EndpointOpts cfg describes, and the TLS
// settings of its cloud entry when it names one.
func (cfg Config) resolve() (gophercloud.AuthOptions, gophercloud.EndpointOpts, *tls.Config, error) {
	iface, err := availability(cfg.Interface)
	if err != nil {
		return gophercloud.AuthOptions{}, gophercloud.EndpointOpts{}, nil, err
	}
	switch {
	case cfg.AuthOptions != nil && cfg.Cloud != "":
		return gophercloud.AuthOptions{}, gophercloud.EndpointOpts{}, nil,
			errors.New("openstack.Config: set AuthOptions or Cloud, not both")
	case cfg.AuthOptions != nil:
		return *cfg.AuthOptions, gophercloud.EndpointOpts{Region: cfg.Region, Availability: iface}, nil, nil
	case cfg.Cloud == "":
		return gophercloud.AuthOptions{}, gophercloud.EndpointOpts{}, nil,
			errors.New("openstack.Config: set AuthOptions or Cloud")
	case len(cfg.CloudsYAML) == 0:
		return gophercloud.AuthOptions{}, gophercloud.EndpointOpts{}, nil,
			fmt.Errorf("openstack.Config: cloud %q needs CloudsYAML", cfg.Cloud)
	}
	// Every setting Parse would otherwise take from OS_CLOUD, OS_REGION_NAME,
	// OS_INTERFACE or OS_CLIENT_CONFIG_FILE is passed explicitly, so none of
	// them is read.
	parse := []clouds.ParseOption{
		clouds.WithCloudName(cfg.Cloud),
		clouds.WithCloudsYAML(bytes.NewReader(cfg.CloudsYAML)),
		clouds.WithRegion(cfg.Region),
		clouds.WithEndpointType(string(iface)),
	}
	if len(cfg.SecureYAML) > 0 {
		parse = append(parse, clouds.WithSecureYAML(bytes.NewReader(cfg.SecureYAML)))
	}
	ao, eo, tlsCfg, err := clouds.Parse(parse...)
	if err != nil {
		return ao, eo, nil, fmt.Errorf("loading cloud %q: %w", cfg.Cloud, err)
	}
	// A pre-issued token cannot mint a fresh one, and gophercloud refuses to
	// try without a new scope.
	ao.AllowReauth = ao.TokenID == ""
	return ao, eo, tlsCfg, nil
}

// availability maps a Config.Interface onto gophercloud's spelling.
func availability(iface string) (gophercloud.Availability, error) {
	switch a := gophercloud.Availability(iface); a {
	case "", gophercloud.AvailabilityPublic, gophercloud.AvailabilityInternal, gophercloud.AvailabilityAdmin:
		return a, nil
	}
	return "", fmt.Errorf("openstack.Config: unsupported Interface %q, want public, internal or admin", iface)
}
