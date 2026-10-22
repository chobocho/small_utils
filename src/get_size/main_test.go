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

func TestResolveOptions(t *testing.T) {
	// Default when run without option: no args, no flags set
	opts, targets := resolveOptions(options{maxDepth: -1}, []string{}, false, false)
	if len(targets) != 1 || targets[0] != "." {
		t.Fatalf("targets = %#v, want [\".\"]", targets)
	}
	if !opts.showDirs {
		t.Fatal("expected showDirs to be true")
	}
	if opts.maxDepth != 1 {
		t.Fatalf("maxDepth = %d, want 1", opts.maxDepth)
	}
	if !opts.human || !opts.sortSize {
		t.Fatalf("expected human-readable, size-sorted defaults; got human=%v, sortSize=%v", opts.human, opts.sortSize)
	}

	// Passing "." explicitly without option
	opts, targets = resolveOptions(options{maxDepth: -1}, []string{"."}, false, false)
	if len(targets) != 1 || targets[0] != "." {
		t.Fatalf("targets = %#v, want [\".\"]", targets)
	}
	if !opts.showDirs || opts.maxDepth != 1 || !opts.human || !opts.sortSize {
		t.Fatalf("expected depth-1, human-readable, size-sorted breakdown; got showDirs=%v, maxDepth=%d, human=%v, sortSize=%v", opts.showDirs, opts.maxDepth, opts.human, opts.sortSize)
	}

	// Passing -d explicitly
	opts, targets = resolveOptions(options{showDirs: true, maxDepth: -1}, []string{}, true, false)
	if len(targets) != 1 || targets[0] != "." {
		t.Fatalf("targets = %#v, want [\".\"]", targets)
	}
	if opts.maxDepth != -1 {
		t.Fatalf("maxDepth = %d, want -1", opts.maxDepth)
	}

	// Passing --max-depth 2 explicitly
	opts, targets = resolveOptions(options{maxDepth: 2}, []string{}, false, true)
	if !opts.showDirs || opts.maxDepth != 2 {
		t.Fatalf("expected showDirs=true, maxDepth=2; got showDirs=%v, maxDepth=%d", opts.showDirs, opts.maxDepth)
	}

	// Passing another directory operand
	opts, targets = resolveOptions(options{maxDepth: -1}, []string{"somedir"}, false, false)
	if len(targets) != 1 || targets[0] != "somedir" {
		t.Fatalf("targets = %#v, want [\"somedir\"]", targets)
	}
	if opts.showDirs {
		t.Fatal("expected showDirs to be false for specified directory without -d")
	}
}

func TestDisplayDirectoryPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reports")
	child := filepath.Join(root, "Downloads")

	if got, want := displayDirectoryPath(root, root), root+string(filepath.Separator); got != want {
		t.Errorf("root label = %q, want %q", got, want)
	}
	if got, want := displayDirectoryPath(root, child), "Downloads"+string(filepath.Separator); got != want {
		t.Errorf("child label = %q, want %q", got, want)
	}
}

func TestScanRootMaxDepthOne(t *testing.T) {
	root := t.TempDir()
	sub1 := filepath.Join(root, "sub1")
	sub2 := filepath.Join(sub1, "sub2")
	sub3 := filepath.Join(root, "sub3")

	for _, d := range []string{sub1, sub2, sub3} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(root, "root.txt"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub1, "a.txt"), []byte("1234567890"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub2, "b.txt"), []byte("12345678901234567890"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub3, "c.txt"), []byte("123456789012345678901234567890"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts := options{
		showDirs: true,
		maxDepth: 1,
	}
	got, err := scanRoot(root, opts)
	if err != nil {
		t.Fatal(err)
	}

	// Total bytes: 5 + 10 + 20 + 30 = 65
	if got.Bytes != 65 {
		t.Fatalf("got.Bytes = %d, want 65", got.Bytes)
	}

	// Directories should only have sub1 (30 bytes: a.txt + b.txt) and sub3 (30 bytes: c.txt)
	if len(got.Directories) != 2 {
		t.Fatalf("got %d directories, want 2: %#v", len(got.Directories), got.Directories)
	}
	if got.Directories[0].Path != sub1 || got.Directories[0].Bytes != 30 {
		t.Errorf("Directories[0] = %#v, want Path=%s, Bytes=30", got.Directories[0], sub1)
	}
	if got.Directories[1].Path != sub3 || got.Directories[1].Bytes != 30 {
		t.Errorf("Directories[1] = %#v, want Path=%s, Bytes=30", got.Directories[1], sub3)
	}
}
