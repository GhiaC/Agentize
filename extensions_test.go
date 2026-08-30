package agentize

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghiac/agentize/engine"
)

type extensionFunc func(*Agentize) error

func (f extensionFunc) Attach(ag *Agentize) error { return f(ag) }

func TestUseExtension(t *testing.T) {
	ag := &Agentize{engine: nil}
	if err := ag.Use(extensionFunc(func(*Agentize) error { return nil })); err == nil {
		t.Fatal("expected an uninitialized Agentize error")
	}

	var nilExtension Extension
	ag = &Agentize{engine: &engine.Engine{}}
	if err := ag.Use(nilExtension); err == nil {
		t.Fatal("expected nil extension error")
	}

	want := errors.New("bad extension config")
	err := ag.Use(extensionFunc(func(got *Agentize) error {
		if got != ag {
			t.Fatal("extension received another Agentize instance")
		}
		return want
	}))
	if err == nil || !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("Use() error = %v, want wrapped %v", err, want)
	}
}
