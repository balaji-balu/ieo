package main

import (
	"slices"
	"testing"
)

const module = "github.com/balaji-balu/ieo"

var all = []string{
	module + "/cmd/co",
	module + "/internal/config",
	module + "/internal/en/plugins/wasm",
	module + "/internal/lo",
	module + "/internal/lo/boltstore",
	module + "/internal/lo/reconciler",
	module + "/tests/e2e",
}

func TestSelectPackages(t *testing.T) {
	tests := []struct {
		mode string
		want []string
	}{
		{"build", []string{
			"./cmd/co",
			"./internal/config",
			"./internal/lo",
			"./internal/lo/boltstore",
			"./internal/lo/reconciler",
		}},
		{"test", []string{"./cmd/co", "./internal/lo"}},
		{"no-test", []string{
			"./internal/config",
			"./internal/lo/boltstore",
			"./internal/lo/reconciler",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			got, err := selectPackages(tt.mode, module, all)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// A path that merely starts with a skipped one is a different package.
func TestSelectPackages_ExactMatchOnly(t *testing.T) {
	got, err := selectPackages("test", module, []string{module + "/internal/configx"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"./internal/configx"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSelectPackages_UnknownMode(t *testing.T) {
	if _, err := selectPackages("lint", module, all); err == nil {
		t.Error("want error for unknown mode")
	}
}
