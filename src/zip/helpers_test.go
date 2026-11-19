package main

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// runCLI는 프로세스를 띄우지 않고 run()을 직접 호출해 종료 코드와 출력을 돌려준다.
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// mustRun은 기대한 종료 코드가 아니면 즉시 실패시킨다.
func mustRun(t *testing.T, want int, args ...string) (string, string) {
	t.Helper()
	code, stdout, stderr := runCLI(t, args...)
	if code != want {
		t.Fatalf("czip %s: exit %d, want %d\nstdout: %s\nstderr: %s",
			strings.Join(args, " "), code, want, stdout, stderr)
	}
	return stdout, stderr
}

// chdir는 go.mod의 go 1.20 호환을 위해 t.Chdir 대신 직접 구현했다.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// pseudoRandom은 시드가 고정된 압축 불가능 데이터를 만든다(테스트 재현성).
func pseudoRandom(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

// writeTree: 이름이 "/"로 끝나면 디렉터리, 아니면 파일로 만든다.
func writeTree(t *testing.T, root string, tree map[string]string) {
	t.Helper()
	for name, body := range tree {
		p := filepath.Join(root, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// readTree: 디렉터리는 "이름/" → "", 파일은 "이름" → 내용으로 읽어 온다.
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			got[rel+"/"] = ""
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		got[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func assertTreeEqual(t *testing.T, got, want map[string]string) {
	t.Helper()
	for name, body := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("missing %q", name)
			continue
		}
		if g != body {
			t.Errorf("%q: content differs (got %d bytes, want %d bytes)", name, len(g), len(body))
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected %q", name)
		}
	}
}

// sampleTree: 빈 파일, 빈 폴더, 한글 이름, 깊은 경로, 압축 가능/불가능 데이터를 고루 담는다.
// 파일 5개(합계 25019바이트), 디렉터리 5개.
func sampleTree() map[string]string {
	return map[string]string{
		"hello.txt":          "hello\n",
		"empty.txt":          "",
		"emptydir/":          "",
		"한글 폴더/한글파일.txt":     "가나다라\n",
		"deep/a/b/data.bin":  string(pseudoRandom(5000, 1)),
		"deep/a/letters.txt": strings.Repeat("a", 20000),
	}
}

const (
	sampleFiles = 5
	sampleDirs  = 5
	sampleBytes = 25019
)

func makeSource(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	writeTree(t, src, sampleTree())
	return src
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

type zipEntry struct {
	name  string
	body  string
	mode  fs.FileMode // 0이면 일반 파일 0644
	flags uint16
}

// buildZip은 czip이 스스로는 만들지 않는 엔트리(탈출 경로, 심링크, 암호화 플래그)를
// 담은 아카이브를 표준 라이브러리로 직접 만든다.
func buildZip(t *testing.T, p string, entries []zipEntry) {
	t.Helper()
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		fh := &zip.FileHeader{Name: e.name, Method: zip.Store, Flags: e.flags}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		fh.SetMode(mode)
		w, err := zw.CreateHeader(fh)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// archiveNames는 (분할 여부와 무관하게) 아카이브의 엔트리 이름을 정렬해 돌려준다.
func archiveNames(t *testing.T, name string) []string {
	t.Helper()
	a, err := openArchive(name)
	if err != nil {
		t.Fatalf("openArchive(%q): %v", name, err)
	}
	defer a.Close()
	var names []string
	for _, f := range a.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}
