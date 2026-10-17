package shell

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ftarasenko/go-openstackclient/internal/kochome"
)

// An embedding host records every line a console user runs as an audit event.
// koc writes one JSON object per line the shell runs — built-ins and refused
// lines included — to a file descriptor the host passed in (KOC_AUDIT_FD) or a
// file (KOC_AUDIT_FILE). The command's output is never part of an event, and a
// secret flag's value is replaced by "***" wherever it appears on the line,
// by the same key list --debug redacts with.
//
// If the descriptor or file fails, koc warns once on stderr and carries on:
// failing closed — refusing to run unaudited — is the host's decision, made by
// watching its end of the pipe.

// Environment variables naming the audit sink.
const (
	EnvAuditFD   = "KOC_AUDIT_FD"
	EnvAuditFile = "KOC_AUDIT_FILE"
)

// Refusal reasons the shell itself records; the command tree's own (the
// embedded profile's) arrive through refusal.
const (
	refusedSyntax  = "syntax"
	refusedExpired = "expired"
	refusedScope   = "scope"
	refusedNested  = "nested"
)

// refusal is implemented by an error that refused a line before it ran (the
// embedded profile's cli.RefusedError).
type refusal interface{ RefusalReason() string }

// auditEvent is one line of the audit log. The field set is the contract with
// the host (docs/shell.md); add to it, never rename.
type auditEvent struct {
	TS         string   `json:"ts"`
	Argv       []string `json:"argv"`
	Command    string   `json:"command"`
	Region     string   `json:"region"`
	ProjectID  string   `json:"project_id"`
	ExitCode   int      `json:"exit_code"`
	DurationMS int64    `json:"duration_ms"`
	Refused    string   `json:"refused,omitempty"`
}

// auditLog writes events; a nil *auditLog writes nothing.
type auditLog struct {
	mu     sync.Mutex
	w      io.WriteCloser
	errOut io.Writer
}

// openAudit opens the sink KOC_AUDIT_FD or KOC_AUDIT_FILE names, or returns
// nil when neither is set. A sink that cannot be opened is warned about once.
func openAudit(errOut io.Writer) *auditLog {
	a := &auditLog{errOut: errOut}
	if v := strings.TrimSpace(os.Getenv(EnvAuditFD)); v != "" {
		fd, err := strconv.ParseUint(v, 10, 31)
		if err != nil || fd < 3 {
			a.warn(fmt.Errorf("%s=%q is not a file descriptor above 2", EnvAuditFD, v))
			return nil
		}
		a.w = os.NewFile(uintptr(fd), "audit")
		return a
	}
	if p := os.Getenv(EnvAuditFile); p != "" {
		f, err := kochome.OpenFile(kochome.Resolve(p), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			a.warn(err)
			return nil
		}
		a.w = f
		return a
	}
	return nil
}

func (a *auditLog) warn(err error) {
	_, _ = fmt.Fprintf(a.errOut, "koc shell: warning: audit log: %v; continuing without it\n", err)
}

// write records ev as one line, in a single write so concurrent writers to a
// shared pipe cannot interleave inside it.
func (a *auditLog) write(ev auditEvent) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.w == nil {
		return
	}
	if ev.Argv == nil {
		ev.Argv = []string{}
	}
	b, err := json.Marshal(ev)
	if err == nil {
		_, err = a.w.Write(append(b, '\n'))
	}
	if err != nil {
		a.warn(err)
		_ = a.w.Close()
		a.w = nil
	}
}

func (a *auditLog) close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.w != nil {
		_ = a.w.Close()
		a.w = nil
	}
}

// lineRecord collects what an audit event needs while a line runs.
type lineRecord struct {
	start     time.Time
	argv      []string
	command   string
	region    string
	projectID string
	refused   string
}
