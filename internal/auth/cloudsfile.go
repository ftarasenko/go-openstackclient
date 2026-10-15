package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gophercloud/gophercloud/v2/openstack/config/clouds"
	"gopkg.in/yaml.v3"
)

// clouds.yaml discovery, following openstacksdk's loader
// (openstack/config/loader.py, 4.21.0) rather than gophercloud's, which differs
// in four ways an operator notices:
//
//   - OS_CLIENT_CONFIG_FILE is *prepended* to the search list upstream, so a
//     path that does not exist falls back to the usual locations; gophercloud
//     makes it the only location.
//   - Upstream accepts clouds.yaml, clouds.yml and clouds.json in every
//     directory; gophercloud only clouds.yaml.
//   - The directory list includes the platform's site config directory
//     (/etc/xdg/openstack on Linux) and, on macOS and Windows, the platform user
//     config directory as well as ~/.config/openstack.
//   - secure.yaml is searched on its own list, headed by OS_CLIENT_SECURE_FILE,
//     instead of only next to the clouds.yaml that was found.
//
// The files are still parsed by gophercloud's clouds.Parse; koc only decides
// which ones it reads.

const (
	envClientConfigFile = "OS_CLIENT_CONFIG_FILE"
	envClientSecureFile = "OS_CLIENT_SECURE_FILE"
)

// configSuffixes are the extensions openstacksdk tries, in its order. YAML is a
// superset of JSON, so one decoder reads all three.
var configSuffixes = []string{".yaml", ".yml", ".json"}

// configSearchDirs is openstacksdk's CONFIG_SEARCH_PATH: the working directory,
// the platform user config directory, ~/.config/openstack, the platform site
// config directory, and /etc/openstack. Duplicates (on Linux the first two
// user directories usually coincide) are dropped.
func configSearchDirs() []string {
	var dirs []string
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, cwd)
	}
	dirs = append(dirs, userConfigDirs()...)
	dirs = append(dirs, siteConfigDirs()...)
	dirs = append(dirs, "/etc/openstack")

	seen := make(map[string]bool, len(dirs))
	out := dirs[:0]
	for _, d := range dirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// userConfigDirs is platformdirs' user_config_dir("openstack") followed by the
// UNIX ~/.config/openstack openstacksdk always adds (rooted at SNAP_REAL_HOME
// inside a snap, as upstream does).
func userConfigDirs() []string {
	var dirs []string
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			dirs = append(dirs, filepath.Join(d, "OpenStack", "openstack"))
		}
	case "darwin":
		if home != "" {
			dirs = append(dirs, filepath.Join(home, "Library", "Application Support", "openstack"))
		}
	default:
		if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
			dirs = append(dirs, filepath.Join(d, "openstack"))
		} else if home != "" {
			dirs = append(dirs, filepath.Join(home, ".config", "openstack"))
		}
	}
	if snapHome := os.Getenv("SNAP_REAL_HOME"); snapHome != "" {
		home = snapHome
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".config", "openstack"))
	}
	return dirs
}

// siteConfigDirs is platformdirs' site_config_dir("openstack", multipath=True).
func siteConfigDirs() []string {
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("PROGRAMDATA"); d != "" {
			return []string{filepath.Join(d, "OpenStack", "openstack")}
		}
		return nil
	case "darwin":
		return []string{"/Library/Application Support/openstack"}
	}
	xdg := os.Getenv("XDG_CONFIG_DIRS")
	if xdg == "" {
		xdg = "/etc/xdg"
	}
	var dirs []string
	for _, d := range filepath.SplitList(xdg) {
		if d != "" {
			dirs = append(dirs, filepath.Join(d, "openstack"))
		}
	}
	return dirs
}

// configCandidates lists, in search order, every path a config file called
// base (clouds, secure, clouds-public) may live at, headed by the value of
// override when that environment variable is set.
func configCandidates(base, override string) []string {
	var out []string
	if override != "" {
		if p := os.Getenv(override); p != "" {
			out = append(out, p)
		}
	}
	for _, d := range configSearchDirs() {
		for _, s := range configSuffixes {
			out = append(out, filepath.Join(d, base+s))
		}
	}
	return out
}

// firstReadable returns the first candidate that exists and can be read.
// openstacksdk likewise skips a file it is denied access to.
func firstReadable(candidates []string) string {
	for _, p := range candidates {
		f, err := os.Open(p) //nolint:gosec // G304: the clouds.yaml search path, which OS_CLIENT_*_FILE exists to point anywhere
		if err != nil {
			continue
		}
		st, err := f.Stat()
		_ = f.Close()
		if err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// cloudsFile is the clouds.yaml koc found, with what koc itself needs to know
// about it before handing it to clouds.Parse.
type cloudsFile struct {
	path       string
	securePath string
	// names lists the clouds the file (and secure file) define.
	names []string
	// authTypes maps a cloud name to its auth_type, if it sets one.
	authTypes map[string]string
}

// cloudsDoc is the slice of a clouds.yaml koc decodes itself.
type cloudsDoc struct {
	Clouds map[string]struct {
		AuthType string `yaml:"auth_type"`
	} `yaml:"clouds"`
}

// findCloudsFile locates and minimally decodes clouds.yaml and secure.yaml. A
// missing clouds.yaml is not an error: it returns nil, and the caller reports
// the search path if it needed one.
func findCloudsFile() (*cloudsFile, error) {
	path := firstReadable(configCandidates("clouds", envClientConfigFile))
	if path == "" {
		return nil, nil
	}
	cf := &cloudsFile{path: path, securePath: firstReadable(configCandidates("secure", envClientSecureFile)), authTypes: map[string]string{}}

	seen := map[string]bool{}
	for _, p := range []string{cf.path, cf.securePath} {
		if p == "" {
			continue
		}
		doc, err := readCloudsDoc(p)
		if err != nil {
			return nil, err
		}
		for name, c := range doc.Clouds {
			if !seen[name] {
				seen[name] = true
				cf.names = append(cf.names, name)
			}
			if c.AuthType != "" && cf.authTypes[name] == "" {
				cf.authTypes[name] = c.AuthType
			}
		}
	}
	return cf, nil
}

func readCloudsDoc(path string) (cloudsDoc, error) {
	var doc cloudsDoc
	raw, err := os.ReadFile(path) //nolint:gosec // G304: a clouds.yaml / secure.yaml firstReadable chose from the search path
	if err != nil {
		return doc, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return doc, fmt.Errorf("parsing %s: %w", path, err)
	}
	return doc, nil
}

// parseOptions are the clouds.Parse options that point gophercloud at the
// files findCloudsFile chose, plus the public-clouds search list.
func (cf *cloudsFile) parseOptions() ([]clouds.ParseOption, func(), error) {
	opts := []clouds.ParseOption{
		clouds.WithLocations(cf.path),
		clouds.WithPublicLocations(configCandidates("clouds-public", "")...),
	}
	if cf.securePath == "" {
		// gophercloud would otherwise look for a secure.yaml next to the
		// clouds.yaml; upstream does not, unless that directory is on the
		// search list, in which case findCloudsFile already found it.
		opts = append(opts, clouds.WithSecureYAML(strings.NewReader("{}")))
		return opts, func() {}, nil
	}
	f, err := os.Open(cf.securePath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s: %w", cf.securePath, err)
	}
	return append(opts, clouds.WithSecureYAML(f)), func() { _ = f.Close() }, nil
}

// errNoCloudsFile is returned for a named cloud when no clouds.yaml exists on
// the search path.
func errNoCloudsFile() error {
	return fmt.Errorf("no clouds.yaml found; searched %s", strings.Join(configCandidates("clouds", envClientConfigFile), ", "))
}
