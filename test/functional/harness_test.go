//go:build functional

// Package functional runs the koc binary against a real OpenStack — the
// single-node devstack scripts/devstack/up.sh brings up — the way an operator
// does: a separate process, credentials from clouds.yaml or an openrc, output
// parsed from -f json.
//
// Nothing here runs by default. The files carry the "functional" build tag, so
// `go test ./...` (and make test/cover/sonar) never compiles them, and without
// KOC_FT_ENV every test skips. To run them against a stacked devstack:
//
//	make functional KOC_FT_ENV=<log-dir>/functional.env [KOC_BIN=./koc] [KOC_FT_AUTH=env]
//
// up.sh writes functional.env (series, features, backend, fixtures, S3
// credentials) and, next to it, openrc.env (the OS_* of devstack's `openrc
// admin admin`). KOC_FT_AUTH picks the default credential path every suite
// runs with — "clouds" (OS_CLOUD=devstack-admin, the default) or "env" (the
// openrc) — because both are how operators run koc and both deserve the full
// suites; the nightly alternates them across cells. The auth tests exercise
// every path regardless.
//
// The tests gate on what the cloud can do rather than on its release name
// wherever the cloud says so itself (requireExtension, requireFeature), and on
// the series only where it does not (requireSeries).
package functional

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/ftarasenko/go-openstackclient/internal/cli"
)

// cloud is functional.env, parsed.
type cloud struct {
	Series   series
	Features map[string]bool
	Backend  string // "ovs" or "ovn"
	Image    string
	// Cloud is the clouds.yaml entry up.sh points OS_CLOUD at.
	Cloud string
	// Openrc is openrc.env: the OS_* an operator's `source openrc` leaves.
	Openrc []string
	// S3 holds the AWS_* up.sh minted, when the core feature is on.
	S3 []string
	// Auth is the default credential path: "clouds" or "env".
	Auth string
}

var (
	env     *cloud // nil when KOC_FT_ENV is unset: every cloud test skips
	errEnv  error
	recordr = &recorder{hits: map[string]int{}}

	kocOnce sync.Once
	kocBin  string
	errKoc  error
)

func TestMain(m *testing.M) {
	env, errEnv = loadEnv(os.Getenv("KOC_FT_ENV"))
	code := m.Run()
	if env != nil {
		recordr.report()
	}
	if kocBin != "" && os.Getenv("KOC_BIN") == "" {
		_ = os.Remove(kocBin)
	}
	os.Exit(code)
}

// koc returns the binary under test, building it on first use. Tests that need
// only the binary — the Vault CLI against the fake server — therefore run with
// no cloud at all, offline CI included.
func koc(t *testing.T) string {
	t.Helper()
	kocOnce.Do(func() { kocBin, errKoc = buildKoc() })
	if errKoc != nil {
		t.Fatal(errKoc)
	}
	return kocBin
}

// ft returns the cloud, skipping the test when there is none.
func ft(t *testing.T) *cloud {
	t.Helper()
	if errEnv != nil {
		t.Fatalf("KOC_FT_ENV: %v", errEnv)
	}
	if env == nil {
		t.Skip("KOC_FT_ENV is not set; see the package doc to run against a devstack")
	}
	return env
}

// loadEnv parses KEY=VALUE lines. Nothing is evaluated: a value is everything
// after the first "=", verbatim.
func loadEnv(path string) (*cloud, error) {
	if path == "" {
		// No file is not an error: it means "no devstack", and every test skips.
		return nil, nil
	}
	kv, err := readKV(path)
	if err != nil {
		return nil, err
	}
	c := &cloud{
		Features: map[string]bool{},
		Backend:  kv["KOC_FT_BACKEND"],
		Image:    kv["KOC_FT_IMAGE"],
		Cloud:    kv["OS_CLOUD"],
		Auth:     os.Getenv("KOC_FT_AUTH"),
	}
	if c.Series, err = parseSeries(kv["KOC_FT_SERIES"]); err != nil {
		return nil, err
	}
	for _, f := range strings.Split(kv["KOC_FT_FEATURES"], ",") {
		if f = strings.TrimSpace(f); f != "" {
			c.Features[f] = true
		}
	}
	for k, v := range kv {
		if strings.HasPrefix(k, "AWS_") {
			c.S3 = append(c.S3, k+"="+v)
		}
	}
	sort.Strings(c.S3)
	switch c.Auth {
	case "":
		c.Auth = "clouds"
	case "clouds", "env":
	default:
		return nil, fmt.Errorf("KOC_FT_AUTH=%q: want clouds or env", c.Auth)
	}
	if c.Cloud == "" {
		return nil, fmt.Errorf("%s: no OS_CLOUD", path)
	}
	openrc, err := readKV(filepath.Join(filepath.Dir(path), "openrc.env"))
	if err != nil && (c.Auth == "env" || !errors.Is(err, os.ErrNotExist)) {
		return nil, fmt.Errorf("openrc.env: %w", err)
	}
	for k, v := range openrc {
		c.Openrc = append(c.Openrc, k+"="+v)
	}
	sort.Strings(c.Openrc)
	return c, nil
}

func readKV(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	kv := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s: not KEY=VALUE: %q", path, line)
		}
		kv[k] = v
	}
	return kv, sc.Err()
}

// buildKoc returns KOC_BIN, or builds koc offline from vendor/ into a temp dir.
func buildKoc() (string, error) {
	if bin := os.Getenv("KOC_BIN"); bin != "" {
		return filepath.Abs(bin)
	}
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	out := filepath.Join(os.TempDir(), fmt.Sprintf("koc-functional-%d", os.Getpid()))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", out, "./cmd/koc")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=vendor", "GOPROXY=off", "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building koc: %w\n%s", err, b)
	}
	return out, nil
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the test directory")
		}
		dir = parent
	}
}

// --- series ----------------------------------------------------------------------

// series orders OpenStack releases. Zed predates numbered series; it is 2022.2.
type series struct{ year, n int }

func parseSeries(s string) (series, error) {
	if s == "zed" {
		return series{2022, 2}, nil
	}
	y, n, ok := strings.Cut(s, ".")
	yi, err1 := strconv.Atoi(y)
	ni, err2 := strconv.Atoi(n)
	if !ok || err1 != nil || err2 != nil {
		return series{}, fmt.Errorf("KOC_FT_SERIES=%q: want zed or YYYY.N", s)
	}
	return series{yi, ni}, nil
}

func (s series) String() string {
	if s == (series{2022, 2}) {
		return "zed"
	}
	return fmt.Sprintf("%d.%d", s.year, s.n)
}

func (s series) atLeast(minimum string) bool {
	m, err := parseSeries(minimum)
	if err != nil {
		panic(err)
	}
	return s.year > m.year || (s.year == m.year && s.n >= m.n)
}

// --- gates -----------------------------------------------------------------------

func requireFeature(t *testing.T, f string) {
	t.Helper()
	if !ft(t).Features[f] {
		t.Skipf("devstack stacked without the %q feature", f)
	}
}

// requireSeries is for behaviour nothing on the cloud advertises; prefer
// requireExtension where an extension says it.
func requireSeries(t *testing.T, minimum string) {
	t.Helper()
	if s := ft(t).Series; !s.atLeast(minimum) {
		t.Skipf("needs %s or later; this cloud is %s", minimum, s)
	}
}

// requireBackend is for data-plane assertions only; the API is the same on both.
func requireBackend(t *testing.T, b string) {
	t.Helper()
	if got := ft(t).Backend; got != b {
		t.Skipf("needs ML2/%s; this cloud runs ML2/%s", strings.ToUpper(b), strings.ToUpper(got))
	}
}

var (
	extOnce sync.Once
	extSet  map[string]bool
	errExt  error
)

// extensions is the cloud's neutron extension aliases, read once through koc.
func extensions(t *testing.T) map[string]bool {
	t.Helper()
	extOnce.Do(func() {
		var rows []map[string]any
		res := defaultRunner(ft(t)).run(t, "network", "extension", "list", "-f", "json")
		if res.code != 0 {
			errExt = fmt.Errorf("koc network extension list: exit %d: %s", res.code, res.stderr)
			return
		}
		if err := json.Unmarshal([]byte(res.stdout), &rows); err != nil {
			errExt = err
			return
		}
		extSet = map[string]bool{}
		for _, r := range rows {
			if a, ok := r["Alias"].(string); ok {
				extSet[a] = true
			}
		}
	})
	if errExt != nil {
		t.Fatal(errExt)
	}
	return extSet
}

func requireExtension(t *testing.T, alias string) {
	t.Helper()
	if !extensions(t)[alias] {
		t.Skipf("neutron has no %q extension on this cloud", alias)
	}
}

// --- running koc ---------------------------------------------------------------

// runner runs koc with an environment built from scratch: the process's own,
// minus every variable koc reads credentials or cloud selection from, plus
// exactly what the test asks for. A test never inherits a credential by
// accident.
type runner struct {
	env     []string
	stdin   string
	timeout time.Duration
}

// scrubbed are the variable families koc reads credentials from.
var scrubbed = []string{"OS_", "AWS_", "S3_", "s3_", "VAULT_", "KOC_", "KUBECONFIG"}

func baseEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		keep := true
		for _, p := range scrubbed {
			if strings.HasPrefix(kv, p) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

// cloudsRunner authenticates from clouds.yaml alone.
func cloudsRunner(c *cloud) runner {
	return runner{env: append(baseEnv(), "OS_CLOUD="+c.Cloud)}
}

// openrcRunner authenticates from devstack's openrc alone.
func openrcRunner(c *cloud) runner {
	return runner{env: append(baseEnv(), c.Openrc...)}
}

// defaultRunner is the credential path KOC_FT_AUTH selects for the suites.
func defaultRunner(c *cloud) runner {
	if c.Auth == "env" {
		return openrcRunner(c)
	}
	return cloudsRunner(c)
}

func (r runner) with(kv ...string) runner {
	r.env = append(append([]string(nil), r.env...), kv...)
	return r
}

func (r runner) withStdin(s string) runner {
	r.stdin = s
	return r
}

type result struct {
	stdout, stderr string
	code           int
}

func (r runner) run(t *testing.T, args ...string) result {
	t.Helper()
	recordr.hit(args)
	timeout := r.timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	// Not t.Context(): it is cancelled before t.Cleanup functions run, and
	// cleanups are where tests delete what they created.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, koc(t), args...)
	cmd.Env = r.env
	// No input means no stdin at all (/dev/null), as a cron job or a terminal
	// gives: an empty pipe is data to koc, as it is upstream, and image create
	// would upload it.
	if r.stdin != "" {
		cmd.Stdin = strings.NewReader(r.stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := result{stdout: stdout.String(), stderr: stderr.String()}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		res.code = exit.ExitCode()
	default:
		t.Fatalf("running koc %s: %v", strings.Join(args, " "), err)
	}
	return res
}

// ok runs koc and fails the test unless it exits 0.
func (r runner) ok(t *testing.T, args ...string) string {
	t.Helper()
	res := r.run(t, args...)
	if res.code != 0 {
		t.Fatalf("koc %s: exit %d\nstderr: %s", strings.Join(args, " "), res.code, res.stderr)
	}
	return res.stdout
}

// json runs koc with -f json and decodes its output into v.
func (r runner) json(t *testing.T, v any, args ...string) {
	t.Helper()
	out := r.ok(t, append(args, "-f", "json")...)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("koc %s -f json: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// fails runs koc, fails the test if it exits 0, and returns its stderr.
func (r runner) fails(t *testing.T, args ...string) string {
	t.Helper()
	res := r.run(t, args...)
	if res.code == 0 {
		t.Fatalf("koc %s: exit 0, want a failure\nstdout: %s", strings.Join(args, " "), res.stdout)
	}
	return res.stderr
}

// --- which commands ran -------------------------------------------------------

// recorder counts which leaf commands the suite executed, resolved against the
// real command tree so arguments and flags do not count as command words.
type recorder struct {
	mu   sync.Mutex
	hits map[string]int
	once sync.Once
	root *cobra.Command
}

func (r *recorder) hit(args []string) {
	r.once.Do(func() { r.root = cli.NewRootCommand("functional") })
	cmd := resolveLeaf(r.root, args) // the same resolution TestEveryLeafIsCovered uses
	if cmd == nil {
		return
	}
	r.mu.Lock()
	r.hits[cmd.CommandPath()]++
	r.mu.Unlock()
}

// report prints how much of the command tree the run executed, and appends it
// to the GitHub step summary when there is one.
func (r *recorder) report() {
	r.once.Do(func() { r.root = cli.NewRootCommand("functional") })
	var leaves int
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			if sub.HasSubCommands() {
				walk(sub)
			} else {
				leaves++
			}
		}
	}
	walk(r.root)
	line := fmt.Sprintf("functional (%s, %s, auth=%s): %d of %d leaf commands exercised",
		env.Series, env.Backend, env.Auth, len(r.hits), leaves)
	fmt.Println(line)
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0); err == nil {
			_, _ = fmt.Fprintf(f, "\n%s\n", line)
			_ = f.Close()
		}
	}
}

// --- small helpers for the suites ------------------------------------------------

// uniq returns a resource name no other run or test will pick: tests create
// real objects on a shared cloud, and a fixed name collides across reruns.
func uniq(prefix string) string {
	return fmt.Sprintf("ft-%s-%d", prefix, time.Now().UnixNano())
}

// field reads a key from a -f json object, ignoring case: koc's single-resource
// views mix "Name"-style and "name"-style keys by command, and a suite asserts
// on what a field says, not how it is capitalised.
func field(m map[string]any, key string) string {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			if v == nil {
				return ""
			}
			switch v := v.(type) {
			case string:
				return v
			case float64:
				// encoding/json decodes every number as float64, and %v prints
				// 1048576 as 1.048576e+06.
				return strconv.FormatFloat(v, 'f', -1, 64)
			}
			return fmt.Sprint(v)
		}
	}
	return ""
}

// column collects one column of a -f json listing, ignoring the key's case.
func column(rows []map[string]any, key string) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, field(r, key))
	}
	return out
}

// show runs a single-resource command with -f json and returns the object.
func (r runner) show(t *testing.T, args ...string) map[string]any {
	t.Helper()
	var m map[string]any
	r.json(t, &m, args...)
	return m
}

// list runs a listing command with -f json and returns its rows.
func (r runner) list(t *testing.T, args ...string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	r.json(t, &rows, args...)
	return rows
}
