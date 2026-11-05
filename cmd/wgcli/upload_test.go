package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestExactReader(t *testing.T) {
	src := strings.NewReader("abcdef")
	got, err := io.ReadAll(&exactReader{r: src, want: 4})
	if err != nil || string(got) != "abcd" {
		t.Fatalf("first chunk = %q, %v", got, err)
	}
	got, err = io.ReadAll(&exactReader{r: src, want: 4})
	if !errors.Is(err, io.ErrUnexpectedEOF) || string(got) != "ef" {
		t.Fatalf("short chunk = %q, %v; want ErrUnexpectedEOF", got, err)
	}
	if got, err := io.ReadAll(&exactReader{r: src, want: 0}); err != nil || len(got) != 0 {
		t.Fatalf("empty chunk = %q, %v", got, err)
	}
}
