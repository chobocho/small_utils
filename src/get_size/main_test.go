package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"100", 100},
		{"1K", 1024},
		{"1.5M", 1572864},
		{"2GiB", 2 << 30},
		{"1.1B", 2},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := parseSize(test.input)
			if err != nil {
				t.Fatalf("parseSize(%q): %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("parseSize(%q) = %d, want %d", test.input, got, test.want)
			}
		})
	}
	for _, input := range []string{"", "-1M", "2XB"} {
		if _, err := parseSize(input); err == nil {
			t.Errorf("parseSize(%q) unexpectedly succeeded", input)
		}
	}
}

func TestScanRootLargeItems(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.bin"), []byte("123456"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "small.txt"), []byte("12"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts := options{
		largeAll: true,
		minSize:  sizeValue{bytes: 5, set: true},
	}
	got, err := scanRoot(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bytes != 8 {
		t.Fatalf("root total = %d, want 8", got.Bytes)
	}
	if len(got.LargeFiles) != 1 || got.LargeFiles[0].Path != filepath.Join(root, "large.bin") {
		t.Fatalf("large files = %#v, want only large.bin", got.LargeFiles)
	}
	if len(got.LargeDirectories) != 1 || got.LargeDirectories[0].Path != root {
		t.Fatalf("large directories = %#v, want only root directory", got.LargeDirectories)
	}
}
