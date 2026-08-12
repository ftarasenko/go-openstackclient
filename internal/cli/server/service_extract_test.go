package server

import (
	"strings"
	"testing"
)

// fakeServicePage is a minimal pagination.Page that is deliberately not a
// services.ServicePage, exercising extractServiceExt's guard against a page
// type it wasn't built to decode.
type fakeServicePage struct{}

func (fakeServicePage) NextPageURL() (string, error) { return "", nil }
func (fakeServicePage) IsEmpty() (bool, error)       { return true, nil }
func (fakeServicePage) GetBody() any                 { return nil }

func TestExtractServiceExt_WrongPageTypeErrors(t *testing.T) {
	_, err := extractServiceExt(fakeServicePage{})
	if err == nil {
		t.Fatal("expected an error for an unexpected page type, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected page type") {
		t.Errorf("error = %q, want it to mention the unexpected page type", err.Error())
	}
}
