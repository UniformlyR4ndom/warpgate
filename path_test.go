package main

import (
	"testing"
)

func TestMakePath(t *testing.T) {
	root := "/srv/files"

	tests := []struct {
		path    string
		want    string
		wantErr bool
	}{
		{"", root, false},
		{"foo.txt", root + "/foo.txt", false},
		{"sub/dir/file.txt", root + "/sub/dir/file.txt", false},
		{"../secret", "", true},
		{"foo/../../../etc/passwd", "", true},
		{`foo\..\bar`, "", true},
		{`..\\evil`, "", true},
	}

	for _, tt := range tests {
		got, err := makePath(root, tt.path)
		if tt.wantErr {
			if err == nil {
				t.Errorf("makePath(%q): expected error, got %q", tt.path, got)
			}
		} else {
			if err != nil {
				t.Errorf("makePath(%q): unexpected error: %v", tt.path, err)
			} else if got != tt.want {
				t.Errorf("makePath(%q): got %q, want %q", tt.path, got, tt.want)
			}
		}
	}
}
