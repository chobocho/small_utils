package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSafeEntryPath(t *testing.T) {
	good := []struct{ in, want string }{
		{"a.txt", "a.txt"},
		{"dir/a.txt", "dir/a.txt"},
		{"dir/", "dir"},
		{"./a", "a"},
		{"a/./b", "a/b"},
		{"a//b", "a/b"},
		{`a\b.txt`, "a/b.txt"}, // 윈도우 도구가 남긴 역슬래시 구분자
		{"한글/파일.txt", "한글/파일.txt"},
		{"..a/b..", "..a/b.."}, // 점 두 개로 시작할 뿐 상위 폴더가 아니다
	}
	for _, tc := range good {
		got, err := safeEntryPath(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("safeEntryPath(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}

	bad := []string{
		"", ".", "/", "./",
		"/abs", "//server/share",
		"..", "../x", "a/../../x", "a/..",
		"a/../b", // 결과는 안쪽이지만 '..' 성분 자체를 허용하지 않는다
		`..\x`, `a\..\..\x`, `\abs`,
		"a\x00b",
	}
	for _, in := range bad {
		if got, err := safeEntryPath(in); err == nil {
			t.Errorf("safeEntryPath(%q) = %q, want error", in, got)
		}
	}
}

func TestMatchEntry(t *testing.T) {
	tests := []struct {
		patterns []string
		name     string
		want     bool
	}{
		{nil, "anything/at/all.txt", true}, // 패턴이 없으면 전부
		{[]string{"*.txt"}, "a.txt", true},
		{[]string{"*.txt"}, "dir/sub/a.txt", true}, // '/' 없는 패턴은 깊이와 무관하게 파일 이름에 적용
		{[]string{"*.txt"}, "dir/a.bin", false},
		{[]string{"a.txt"}, "A.txt", false}, // 대소문자 구분
		{[]string{"dir/*.txt"}, "dir/a.txt", true},
		{[]string{"dir/*.txt"}, "dir/sub/a.txt", false}, // '*' 는 '/' 를 넘지 않는다
		{[]string{"dir/*.txt"}, "other/a.txt", false},
		{[]string{"dir"}, "dir/", true}, // 폴더를 고르면 그 아래 전부
		{[]string{"dir"}, "dir/a.txt", true},
		{[]string{"dir"}, "dir/sub/x.bin", true},
		{[]string{"dir/"}, "dir/sub/x.bin", true},
		{[]string{"dir"}, "dir2/a.txt", false},
		{[]string{"dir"}, "xdir/a.txt", false},
		{[]string{"dir/sub"}, "dir/sub/x.bin", true},
		{[]string{"dir/sub"}, "dir/other/x.bin", false},
		{[]string{"sub"}, "dir/sub/x.bin", true}, // 이름만 준 폴더도 깊이와 무관
		{[]string{"d?r"}, "dar/x", true},
		{[]string{"*.go", "*.md"}, "README.md", true}, // 여러 패턴은 OR
		{[]string{"*.go", "*.md"}, "main.c", false},
	}
	for _, tc := range tests {
		if got := matchEntry(tc.patterns, tc.name); got != tc.want {
			t.Errorf("matchEntry(%q, %q) = %v, want %v", tc.patterns, tc.name, got, tc.want)
		}
	}
}

func TestRunExtractFlat(t *testing.T) {
	src := makeSource(t)
	chdir(t, src)
	work := t.TempDir()
	arch := filepath.Join(work, "a.zip")
	mustRun(t, exitOK, "a", arch, ".")

	dest := filepath.Join(work, "flat")
	mustRun(t, exitOK, "e", "-o"+dest, arch)
	tree := sampleTree()
	want := map[string]string{
		"hello.txt":   tree["hello.txt"],
		"empty.txt":   "",
		"한글파일.txt":    tree["한글 폴더/한글파일.txt"],
		"data.bin":    tree["deep/a/b/data.bin"],
		"letters.txt": tree["deep/a/letters.txt"],
	}
	assertTreeEqual(t, readTree(t, dest), want) // 하위 폴더가 하나도 없어야 한다
}

func TestRunExtractPatterns(t *testing.T) {
	src := makeSource(t)
	chdir(t, src)
	work := t.TempDir()
	arch := filepath.Join(work, "a.zip")
	mustRun(t, exitOK, "a", arch, ".")
	tree := sampleTree()

	t.Run("by file glob", func(t *testing.T) {
		dest := filepath.Join(work, "glob")
		mustRun(t, exitOK, "x", "-o"+dest, arch, "*.txt")
		assertTreeEqual(t, readTree(t, dest), map[string]string{
			"hello.txt":          tree["hello.txt"],
			"empty.txt":          "",
			"한글 폴더/":             "",
			"한글 폴더/한글파일.txt":     tree["한글 폴더/한글파일.txt"],
			"deep/":              "",
			"deep/a/":            "",
			"deep/a/letters.txt": tree["deep/a/letters.txt"],
		})
	})
	t.Run("by directory", func(t *testing.T) {
		dest := filepath.Join(work, "dir")
		mustRun(t, exitOK, "x", "-o"+dest, arch, "deep")
		assertTreeEqual(t, readTree(t, dest), map[string]string{
			"deep/":              "",
			"deep/a/":            "",
			"deep/a/b/":          "",
			"deep/a/b/data.bin":  tree["deep/a/b/data.bin"],
			"deep/a/letters.txt": tree["deep/a/letters.txt"],
		})
	})
	t.Run("no match is a warning", func(t *testing.T) {
		dest := filepath.Join(work, "none")
		_, stderr := mustRun(t, exitWarn, "x", "-o"+dest, arch, "*.nomatch")
		if stderr == "" {
			t.Errorf("expected a message on stderr")
		}
	})
}

func TestRunOverwritePolicy(t *testing.T) {
	src := makeSource(t)
	want := readTree(t, src)
	chdir(t, src)
	work := t.TempDir()
	arch := filepath.Join(work, "a.zip")
	dest := filepath.Join(work, "dest")
	mustRun(t, exitOK, "a", arch, ".")
	mustRun(t, exitOK, "x", "-o"+dest, arch)

	hello := filepath.Join(dest, "hello.txt")
	if err := os.WriteFile(hello, []byte("edited by user"), 0o644); err != nil {
		t.Fatal(err)
	}

	// -y 없이는 기존 파일을 건드리지 않고 경고(1)로 끝난다.
	_, stderr := mustRun(t, exitWarn, "x", "-o"+dest, arch)
	if !strings.Contains(stderr, "hello.txt") {
		t.Errorf("stderr should name the skipped file: %q", stderr)
	}
	if b, _ := os.ReadFile(hello); string(b) != "edited by user" {
		t.Errorf("existing file was overwritten without -y: %q", b)
	}

	mustRun(t, exitOK, "x", "-y", "-o"+dest, arch)
	assertTreeEqual(t, readTree(t, dest), want) // 내용 복원 + 임시 파일 잔재 없음
}

// 악의적인 아카이브가 대상 폴더 밖에 파일을 쓰지 못해야 한다(zip-slip).
func TestRunRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "sandbox", "out")
	absTarget := filepath.Join(root, "abs.txt")
	arch := filepath.Join(root, "evil.zip")
	buildZip(t, arch, []zipEntry{
		{name: "ok.txt", body: "ok"},
		{name: "../evil1.txt", body: "x"},
		{name: "sub/../../../evil2.txt", body: "x"},
		{name: `..\..\evil3.txt`, body: "x"},
		{name: filepath.ToSlash(absTarget), body: "x"},
		{name: "after.txt", body: "after"},
	})

	for _, cmd := range []string{"x", "e"} {
		_, stderr := mustRun(t, exitWarn, cmd, "-y", "-o"+dest, arch)
		if !strings.Contains(stderr, "unsafe") {
			t.Errorf("%s: stderr should report unsafe paths: %q", cmd, stderr)
		}
		// 거부된 엔트리 뒤의 정상 엔트리는 계속 풀려야 한다.
		assertTreeEqual(t, readTree(t, dest), map[string]string{"ok.txt": "ok", "after.txt": "after"})
		for _, p := range []string{
			filepath.Join(root, "sandbox", "evil1.txt"),
			filepath.Join(root, "evil2.txt"),
			filepath.Join(root, "evil3.txt"),
			filepath.Join(root, "sandbox", "evil3.txt"),
			absTarget,
		} {
			if exists(p) {
				t.Errorf("%s: file escaped the output directory: %s", cmd, p)
			}
		}
		if got := dirNames(t, root); strings.Join(got, ",") != "evil.zip,sandbox" {
			t.Errorf("%s: root directory = %v, want [evil.zip sandbox]", cmd, got)
		}
	}
}

// 심링크를 복원하면 뒤따르는 엔트리가 링크를 타고 밖으로 나갈 수 있으므로 만들지 않는다.
func TestRunSkipsSymlinkEntries(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "out")
	arch := filepath.Join(root, "link.zip")
	buildZip(t, arch, []zipEntry{
		{name: "link", body: root, mode: fs.ModeSymlink | 0o777},
		{name: "real.txt", body: "real"},
	})
	_, stderr := mustRun(t, exitWarn, "x", "-o"+dest, arch)
	if !strings.Contains(stderr, "link") {
		t.Errorf("stderr should name the skipped link: %q", stderr)
	}
	assertTreeEqual(t, readTree(t, dest), map[string]string{"real.txt": "real"})
}

func TestRunEncryptedEntry(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "out")
	arch := filepath.Join(root, "enc.zip")
	buildZip(t, arch, []zipEntry{{name: "secret.txt", body: "not really encrypted", flags: 0x1}})

	for _, args := range [][]string{{"t", arch}, {"x", "-o" + dest, arch}} {
		_, stderr := mustRun(t, exitError, args...)
		if !strings.Contains(stderr, "encrypted") {
			t.Errorf("%s: stderr should say the entry is encrypted: %q", args[0], stderr)
		}
	}
	if exists(filepath.Join(dest, "secret.txt")) {
		t.Errorf("encrypted entry was written out as if it were plain data")
	}
}

func TestRunRestoresModeAndTime(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	stamp := time.Date(2020, 1, 2, 3, 4, 6, 0, time.UTC)
	// 0600/0700 은 흔한 umask(022, 027, 077)에 깎이지 않아 환경과 무관하게 비교할 수 있다.
	files := map[string]fs.FileMode{"secret.txt": 0o600, "run.sh": 0o700}
	for name, mode := range files {
		if err := os.WriteFile(name, []byte(name), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(name, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	mustRun(t, exitOK, "a", "m.zip", "secret.txt", "run.sh")
	mustRun(t, exitOK, "x", "-oout", "m.zip")

	for name, mode := range files {
		st, err := os.Stat(filepath.Join("out", name))
		if err != nil {
			t.Fatal(err)
		}
		if !st.ModTime().Equal(stamp) {
			t.Errorf("%s: mtime = %v, want %v", name, st.ModTime().UTC(), stamp)
		}
		if runtime.GOOS != "windows" && st.Mode().Perm() != mode {
			t.Errorf("%s: mode = %v, want %v", name, st.Mode().Perm(), mode)
		}
	}
}

// 같은 이름이 여러 폴더에 있을 때 e(평탄화)는 -y 없이는 첫 파일을 지켜야 한다.
func TestRunFlatNameCollision(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	writeTree(t, work, map[string]string{"a/same.txt": "first", "b/same.txt": "second"})
	mustRun(t, exitOK, "a", "c.zip", "a", "b")

	_, stderr := mustRun(t, exitWarn, "e", "-oflat", "c.zip")
	if !strings.Contains(stderr, "same.txt") {
		t.Errorf("stderr should name the colliding file: %q", stderr)
	}
	assertTreeEqual(t, readTree(t, "flat"), map[string]string{"same.txt": "first"})
}
