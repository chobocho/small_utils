package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	if value == "" {
		return errors.New("exclude pattern must not be empty")
	}
	*s = append(*s, value)
	return nil
}

type entryResult struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type rootResult struct {
	Path             string        `json:"path"`
	Bytes            int64         `json:"bytes"`
	Directories      []entryResult `json:"directories,omitempty"`
	LargeFiles       []entryResult `json:"large_files,omitempty"`
	LargeDirectories []entryResult `json:"large_directories,omitempty"`
}

type sizeValue struct {
	bytes int64
	set   bool
}

func (s *sizeValue) String() string {
	if !s.set {
		return ""
	}
	return strconv.FormatInt(s.bytes, 10)
}

func (s *sizeValue) Set(value string) error {
	bytes, err := parseSize(value)
	if err != nil {
		return err
	}
	s.bytes = bytes
	s.set = true
	return nil
}

func parseSize(value string) (int64, error) {
	value = strings.TrimSpace(strings.ToUpper(value))
	if value == "" {
		return 0, errors.New("size must not be empty")
	}

	suffixes := []struct {
		suffix string
		factor float64
	}{
		{"KIB", 1 << 10}, {"KB", 1 << 10}, {"K", 1 << 10},
		{"MIB", 1 << 20}, {"MB", 1 << 20}, {"M", 1 << 20},
		{"GIB", 1 << 30}, {"GB", 1 << 30}, {"G", 1 << 30},
		{"TIB", 1 << 40}, {"TB", 1 << 40}, {"T", 1 << 40},
		{"B", 1},
	}
	factor := float64(1)
	for _, item := range suffixes {
		if strings.HasSuffix(value, item.suffix) {
			factor = item.factor
			value = strings.TrimSuffix(value, item.suffix)
			break
		}
	}

	n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || n < 0 || math.IsInf(n, 0) || math.IsNaN(n) || n*factor >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("invalid size %q (examples: 1024, 10M, 1.5G)", value)
	}
	return int64(math.Ceil(n * factor)), nil
}

type options struct {
	showDirs   bool
	largeFiles bool
	largeDirs  bool
	largeAll   bool
	human      bool
	sortSize   bool
	jsonOutput bool
	maxDepth   int
	minSize    sizeValue
	excludes   stringList
}

func main() {
	var opts options
	var help bool

	flag.BoolVar(&opts.showDirs, "d", false, "show the size of each descendant directory")
	flag.Var(&opts.minSize, "min-size", "minimum size threshold (e.g. 100K, 10M, 1.5G); required with --large-*")
	flag.BoolVar(&opts.largeFiles, "large-files", false, "list files at least --min-size")
	flag.BoolVar(&opts.largeDirs, "large-dirs", false, "list directories at least --min-size (recursive total)")
	flag.BoolVar(&opts.largeAll, "large-all", false, "list qualifying files and directories together")
	flag.BoolVar(&opts.human, "h", false, "print sizes in human-readable units (like ls -h)")
	flag.BoolVar(&opts.sortSize, "S", false, "sort displayed directories by size, largest first")
	flag.BoolVar(&opts.jsonOutput, "json", false, "output results as JSON")
	flag.IntVar(&opts.maxDepth, "max-depth", -1, "limit displayed descendant directory depth (-d only; 0 means none)")
	flag.Var(&opts.excludes, "exclude", "exclude matching relative path or basename (repeatable)")
	flag.BoolVar(&help, "help", false, "show this help")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: get_size [options] DIR...\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Recursively sum regular-file sizes in each directory.\n\nOptions:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\nExamples:\n  get_size -h ~/Downloads\n  get_size -d -S --max-depth 2 folder1 folder2\n  get_size --exclude '*.tmp' --json folder\n")
	}
	flag.Parse()

	if help {
		flag.Usage()
		return
	}
	if opts.maxDepth < -1 {
		fmt.Fprintln(os.Stderr, "get_size: --max-depth must be -1 or greater")
		os.Exit(2)
	}
	largeMode := opts.largeFiles || opts.largeDirs || opts.largeAll
	if largeMode && !opts.minSize.set {
		fmt.Fprintln(os.Stderr, "get_size: --min-size is required with --large-files, --large-dirs, or --large-all")
		os.Exit(2)
	}
	if opts.minSize.set && !largeMode {
		fmt.Fprintln(os.Stderr, "get_size: --min-size requires one of --large-files, --large-dirs, or --large-all")
		os.Exit(2)
	}
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	results := make([]rootResult, 0, flag.NArg())
	failed := false
	for _, root := range flag.Args() {
		result, err := scanRoot(root, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "get_size: %v\n", err)
			failed = true
		}
		if result.Path != "" {
			results = append(results, result)
		}
	}

	if opts.jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			fmt.Fprintf(os.Stderr, "get_size: writing JSON: %v\n", err)
			os.Exit(1)
		}
	} else if largeMode {
		type listedEntry struct {
			entry entryResult
			kind  string
		}
		for _, result := range results {
			entries := make([]listedEntry, 0, len(result.LargeFiles)+len(result.LargeDirectories))
			if opts.largeFiles || opts.largeAll {
				for _, entry := range result.LargeFiles {
					entries = append(entries, listedEntry{entry: entry, kind: "file"})
				}
			}
			if opts.largeDirs || opts.largeAll {
				for _, entry := range result.LargeDirectories {
					entries = append(entries, listedEntry{entry: entry, kind: "dir"})
				}
			}
			sort.SliceStable(entries, func(i, j int) bool {
				if opts.sortSize && entries[i].entry.Bytes != entries[j].entry.Bytes {
					return entries[i].entry.Bytes > entries[j].entry.Bytes
				}
				if entries[i].entry.Path != entries[j].entry.Path {
					return entries[i].entry.Path < entries[j].entry.Path
				}
				return entries[i].kind < entries[j].kind
			})
			for _, item := range entries {
				fmt.Printf("%s\t%s\t%s\n", formatSize(item.entry.Bytes, opts.human), item.kind, item.entry.Path)
			}
		}
	} else {
		for _, result := range results {
			entries := make([]entryResult, 0, len(result.Directories)+1)
			entries = append(entries, entryResult{Path: result.Path, Bytes: result.Bytes})
			entries = append(entries, result.Directories...)
			if opts.sortSize {
				sort.SliceStable(entries, func(i, j int) bool {
					if entries[i].Bytes == entries[j].Bytes {
						return entries[i].Path < entries[j].Path
					}
					return entries[i].Bytes > entries[j].Bytes
				})
			}
			for _, entry := range entries {
				fmt.Printf("%s\t%s\n", formatSize(entry.Bytes, opts.human), entry.Path)
			}
		}
	}

	if failed {
		os.Exit(1)
	}
}

func scanRoot(root string, opts options) (rootResult, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return rootResult{}, fmt.Errorf("%s: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return rootResult{}, fmt.Errorf("%s: refusing to follow symbolic link", root)
	}
	if !info.IsDir() {
		return rootResult{}, fmt.Errorf("%s: not a directory", root)
	}

	dirs := make(map[string]int64)
	var largeFiles []entryResult
	var walkErrs []error
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			walkErrs = append(walkErrs, fmt.Errorf("%s: %w", path, walkErr))
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			walkErrs = append(walkErrs, fmt.Errorf("%s: %w", path, relErr))
			return nil
		}
		if path == root || rel == "." {
			dirs[path] = 0
		} else if isExcluded(rel, opts.excludes) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if entry.IsDir() {
			dirs[path] = 0
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil // Do not follow symlinks or count devices, sockets, and pipes.
		}

		fileInfo, statErr := entry.Info()
		if statErr != nil {
			walkErrs = append(walkErrs, fmt.Errorf("%s: %w", path, statErr))
			return nil
		}
		fileSize := fileInfo.Size()
		if (opts.largeFiles || opts.largeAll) && fileSize >= opts.minSize.bytes {
			largeFiles = append(largeFiles, entryResult{Path: path, Bytes: fileSize})
		}
		for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
			if _, ok := dirs[parent]; ok {
				dirs[parent] += fileSize
			}
			if parent == root || parent == filepath.Dir(parent) {
				break
			}
		}
		return nil
	})
	if err != nil {
		walkErrs = append(walkErrs, err)
	}

	rootSize := dirs[root]
	result := rootResult{Path: root, Bytes: rootSize}
	if opts.showDirs {
		for path, size := range dirs {
			if path == root {
				continue
			}
			if !withinMaxDepth(root, path, opts.maxDepth) {
				continue
			}
			result.Directories = append(result.Directories, entryResult{Path: path, Bytes: size})
		}
		sort.Slice(result.Directories, func(i, j int) bool {
			return result.Directories[i].Path < result.Directories[j].Path
		})
	}
	if opts.largeFiles || opts.largeAll {
		sort.Slice(largeFiles, func(i, j int) bool { return largeFiles[i].Path < largeFiles[j].Path })
		result.LargeFiles = largeFiles
	}
	if opts.largeDirs || opts.largeAll {
		for path, size := range dirs {
			if size < opts.minSize.bytes || !withinMaxDepth(root, path, opts.maxDepth) {
				continue
			}
			result.LargeDirectories = append(result.LargeDirectories, entryResult{Path: path, Bytes: size})
		}
		sort.Slice(result.LargeDirectories, func(i, j int) bool {
			return result.LargeDirectories[i].Path < result.LargeDirectories[j].Path
		})
	}

	return result, errors.Join(walkErrs...)
}

func withinMaxDepth(root, path string, maxDepth int) bool {
	if path == root || maxDepth < 0 {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	depth := len(strings.Split(filepath.Clean(rel), string(filepath.Separator)))
	return depth <= maxDepth
}

func isExcluded(rel string, patterns []string) bool {
	cleanRel := filepath.Clean(rel)
	base := filepath.Base(cleanRel)
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, cleanRel); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, base); matched {
			return true
		}
	}
	return false
}

func formatSize(size int64, human bool) string {
	if !human {
		return fmt.Sprintf("%d", size)
	}
	if size < 1024 {
		return fmt.Sprintf("%dB", size)
	}
	units := []string{"K", "M", "G", "T", "P", "E"}
	value := float64(size)
	for _, unit := range units {
		value /= 1024
		if value < 1024 || unit == "E" {
			return fmt.Sprintf("%.1f%s", value, unit)
		}
	}
	return fmt.Sprintf("%dB", size)
}
