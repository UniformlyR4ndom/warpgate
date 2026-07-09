package main

import "testing"

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"0", 0, false},
		{"1024", 1024, false},
		{"1k", 1024, false},
		{"1K", 1024, false},
		{"2m", 2 * 1024 * 1024, false},
		{"2M", 2 * 1024 * 1024, false},
		{"256M", 256 * 1024 * 1024, false},
		{"", 0, true},
		{"k", 0, true},
		{"m", 0, true},
		{"-1", 0, true},
		{"-1k", 0, true},
		{"1.5M", 0, true},
		{"abc", 0, true},
	}

	for _, tt := range tests {
		got, err := parseByteSize(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseByteSize(%q): expected error, got %d", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseByteSize(%q): unexpected error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseByteSize(%q): got %d, want %d", tt.in, got, tt.want)
		}
	}
}
