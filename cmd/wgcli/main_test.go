package main

import (
	"slices"
	"strings"
	"testing"
)

func TestHoistGlobalFlags(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"download -o x a", "download -o x a"},
		{"-json download -o x a", "download -json -o x a"},
		{"-url http://h -json list", "list -url http://h -json"},
		{"--url=http://h status", "status --url=http://h"},
		{"-json clip new hi", "clip new -json hi"},
		{"-json sim download -total-size 1K", "sim download -json -total-size 1K"},
		{"-json -h", "-h -json"},
		{"-json", ""},
	} {
		got, err := hoistGlobalFlags(strings.Fields(tc.in))
		if err != nil || !slices.Equal(got, strings.Fields(tc.want)) {
			t.Errorf("%q: got %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"-o x download a", "-url"} {
		if _, err := hoistGlobalFlags(strings.Fields(in)); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}
