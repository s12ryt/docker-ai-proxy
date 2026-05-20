package main

import "testing"

// TestVersionVars ensures the build-time injected variables are package-level.
// It also satisfies coverage tooling for packages without tests.
func TestVersionVars(t *testing.T) {
	if version == "" {
		t.Fatal("version should not be empty")
	}
	if commit == "" {
		t.Fatal("commit should not be empty")
	}
}
