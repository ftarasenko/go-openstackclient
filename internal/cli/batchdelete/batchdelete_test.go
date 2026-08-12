package batchdelete

import (
	"errors"
	"testing"
)

func TestEachAttemptsEveryRefAndJoinsFailures(t *testing.T) {
	var attempted []string
	err := Each([]string{"a", "b", "c"}, func(ref string) error {
		attempted = append(attempted, ref)
		if ref == "b" {
			return errors.New("boom: " + ref)
		}
		return nil
	})
	if len(attempted) != 3 {
		t.Fatalf("expected all 3 refs to be attempted, got %v", attempted)
	}
	if err == nil {
		t.Fatal("expected a joined error for the failing ref")
	}
	if got := err.Error(); got != "boom: b" {
		t.Fatalf("expected error to name the failing ref, got %q", got)
	}
}

func TestEachReturnsNilWhenAllSucceed(t *testing.T) {
	if err := Each([]string{"a", "b"}, func(string) error { return nil }); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestEachOnEmptyRefs(t *testing.T) {
	called := false
	if err := Each(nil, func(string) error { called = true; return nil }); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if called {
		t.Fatal("del must not be called for an empty ref list")
	}
}
