package logger

import (
	"testing"
)

func TestNewRejectsInvalidLevel(t *testing.T) {
	_, _, err := New(Config{Level: "not-a-level", Stdout: true})
	if err == nil {
		t.Fatal("expected invalid log level to be rejected")
	}
}

func TestNewRejectsNoOutput(t *testing.T) {
	_, _, err := New(Config{Level: "info"})
	if err == nil {
		t.Fatal("expected logger without outputs to be rejected")
	}
}
