package shell

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func readEvents(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var evs []map[string]any
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		var ev map[string]any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("audit line %q is not one JSON object: %v", sc.Text(), err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func TestAudit_EventPerLine(t *testing.T) {
	t.Setenv("OS_REGION_NAME", "RegionOne")
	t.Setenv("OS_PROJECT_ID", "p1")
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(EnvAuditFD, "")
	t.Setenv(EnvAuditFile, path)
	k := &fakeKoc{behave: map[string]func(context.Context, []string, io.Writer) error{
		"boom": func(context.Context, []string, io.Writer) error { return failure{code: 3} },
	}}
	s := newSession(t, k, strings.NewReader(
		"koc server list --os-password hunter2 --long\r"+
			"boom\r"+
			"server list | grep x --os-password=hunter2\r"+
			"scope\r"+
			"shell\r"+
			"\r"+
			"exit\r"))
	s.run(t)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Fatalf("the audit log holds the password:\n%s", raw)
	}
	evs := readEvents(t, string(raw))
	want := []struct {
		argv    []any
		command string
		code    float64
		refused string
	}{
		{[]any{"server", "list", "--os-password", "***", "--long"}, "server list", 0, ""},
		{[]any{"boom"}, "boom", 3, ""},
		{[]any{"server", "list", "|", "grep", "x", "--os-password=***"}, "", 2, "syntax"},
		{[]any{"scope"}, "scope", 0, ""},
		{[]any{"shell"}, "shell", 2, "nested"},
		{[]any{"exit"}, "exit", 0, ""},
	}
	if len(evs) != len(want) {
		t.Fatalf("%d events, want %d (an empty line is not one):\n%s", len(evs), len(want), raw)
	}
	for i, w := range want {
		ev := evs[i]
		if !reflect.DeepEqual(ev["argv"], w.argv) || ev["exit_code"] != w.code || ev["command"] != w.command {
			t.Errorf("event %d = %v; want argv %v, command %q, exit %v", i, ev, w.argv, w.command, w.code)
		}
		if got, _ := ev["refused"].(string); got != w.refused {
			t.Errorf("event %d refused %q, want %q", i, got, w.refused)
		}
		if ev["region"] != "RegionOne" || ev["project_id"] != "p1" {
			t.Errorf("event %d scope %v/%v", i, ev["region"], ev["project_id"])
		}
		ts, _ := ev["ts"].(string)
		if parsed, err := time.Parse(time.RFC3339, ts); err != nil || parsed.Location() != time.UTC {
			t.Errorf("event %d ts %q is not RFC 3339 UTC: %v", i, ts, err)
		}
		if _, ok := ev["duration_ms"].(float64); !ok {
			t.Errorf("event %d has no duration_ms", i)
		}
		for _, field := range []string{"ts", "argv", "command", "region", "project_id", "exit_code", "duration_ms"} {
			if _, ok := ev[field]; !ok {
				t.Errorf("event %d lacks %q", i, field)
			}
		}
	}
	if strings.Contains(string(raw), "ran server list") {
		t.Error("command output reached the audit log")
	}
}

func TestAudit_FD(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	// The shell owns and closes the descriptor it is handed, as the host's
	// child would; give it its own.
	fd, err := syscall.Dup(int(w.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvAuditFile, "")
	t.Setenv(EnvAuditFD, strconv.Itoa(fd))
	s := newSession(t, &fakeKoc{}, strings.NewReader("server list\r\x04"))
	s.run(t)
	_ = w.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	if evs := readEvents(t, raw); len(evs) != 1 || evs[0]["command"] != "server list" {
		t.Errorf("events over the fd: %s", raw)
	}
}

func TestAudit_BrokenSinkWarnsOnceAndContinues(t *testing.T) {
	for name, env := range map[string][2]string{
		"unopened fd":   {EnvAuditFD, "250"},
		"not a number":  {EnvAuditFD, "three"},
		"missing dir":   {EnvAuditFile, filepath.Join(t.TempDir(), "no", "such", "dir", "audit")},
		"stdout is not": {EnvAuditFD, "1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(EnvAuditFD, "")
			t.Setenv(EnvAuditFile, "")
			t.Setenv(env[0], env[1])
			k := &fakeKoc{}
			s := newSession(t, k, strings.NewReader("server list\rserver show x\r\x04"))
			s.run(t)
			if n := len(k.ran()); n != 2 {
				t.Errorf("ran %d lines, want 2: the shell carries on without the audit log", n)
			}
			if n := strings.Count(s.errOut.String(), "audit log"); n != 1 {
				t.Errorf("warned %d times, want once:\n%s", n, s.errOut.String())
			}
		})
	}
}
