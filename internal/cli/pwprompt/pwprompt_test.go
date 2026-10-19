package pwprompt

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/term"
)

var testWording = Wording{
	Flag: "--x", First: "One: ", Second: "Two: ", ReadErr: "reading the password", Mismatch: "passwords do not match",
}

// answers returns a reader that yields each answer in turn.
func answers(a ...string) func() ([]byte, error) {
	return func() ([]byte, error) {
		next := a[0]
		a = a[1:]
		return []byte(next), nil
	}
}

func TestConfirm(t *testing.T) {
	var w bytes.Buffer
	pw, err := confirm(&w, testWording, answers("s3cret", "s3cret"))
	if err != nil || pw != "s3cret" {
		t.Fatalf("confirm = %q, %v", pw, err)
	}
	// Each prompt is followed by the newline the hidden Enter did not echo.
	if got := w.String(); got != "One: \nTwo: \n" {
		t.Errorf("wrote %q", got)
	}
}

func TestConfirm_Mismatch(t *testing.T) {
	if _, err := confirm(&bytes.Buffer{}, testWording, answers("a", "b")); err == nil || err.Error() != "passwords do not match" {
		t.Fatalf("err = %v, want the mismatch wording", err)
	}
}

func TestConfirm_ReadError(t *testing.T) {
	boom := errors.New("boom")
	for _, failOn := range []int{1, 2} {
		n := 0
		read := func() ([]byte, error) {
			if n++; n == failOn {
				return nil, boom
			}
			return []byte("pw"), nil
		}
		_, err := confirm(&bytes.Buffer{}, testWording, read)
		if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "reading the password: ") {
			t.Errorf("read %d failing: err = %v", failOn, err)
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestConfirm_WriteError(t *testing.T) {
	if _, err := confirm(failWriter{}, testWording, answers("a", "a")); err == nil {
		t.Fatal("want the prompt's write error")
	}
}

// Under go test stdin is not a terminal, so the refusal names the flag.
func TestConfirmed_NotATerminal(t *testing.T) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is a terminal: the prompt would wait for input")
	}
	_, err := Confirmed(&bytes.Buffer{}, testWording)
	if err == nil || err.Error() != "--x needs an interactive terminal; use --password instead" {
		t.Fatalf("err = %v", err)
	}
}
