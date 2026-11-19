package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 다른 ZIP 도구와의 상호 운용 검증. 도구가 없는 환경에서는 건너뛴다.

func requireTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed", name)
	}
	return p
}

func runTool(t *testing.T, dir, tool string, args ...string) {
	t.Helper()
	cmd := exec.Command(tool, args...)
	cmd.Dir = dir
	// 한글 파일명이 로캘 때문에 깨지지 않도록 UTF-8 로 고정한다.
	cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(tool), strings.Join(args, " "), err, out)
	}
}

func Test7zReadsOurSingleArchive(t *testing.T) {
	sevenZip := requireTool(t, "7z")
	src := makeSource(t)
	want := readTree(t, src)
	chdir(t, src)
	work := t.TempDir()
	arch := filepath.Join(work, "ours.zip")
	mustRun(t, exitOK, "a", arch, ".")

	runTool(t, work, sevenZip, "t", arch)
	dest := filepath.Join(work, "dest")
	runTool(t, work, sevenZip, "x", "-y", "-o"+dest, arch)
	assertTreeEqual(t, readTree(t, dest), want)
}

func Test7zReadsOurSplitArchive(t *testing.T) {
	sevenZip := requireTool(t, "7z")
	work, data := makeSplit(t)

	first := filepath.Join(work, "big.zip.001")
	runTool(t, work, sevenZip, "t", first)
	dest := filepath.Join(work, "dest")
	runTool(t, work, sevenZip, "x", "-y", "-o"+dest, first)
	got, err := os.ReadFile(filepath.Join(dest, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("7z extracted different content")
	}
}

func TestWeRead7zSingleArchive(t *testing.T) {
	sevenZip := requireTool(t, "7z")
	src := makeSource(t)
	want := readTree(t, src)
	work := t.TempDir()
	arch := filepath.Join(work, "theirs.zip")
	runTool(t, src, sevenZip, "a", "-tzip", arch, ".")

	mustRun(t, exitOK, "t", arch)
	dest := filepath.Join(work, "dest")
	mustRun(t, exitOK, "x", "-o"+dest, arch)
	assertTreeEqual(t, readTree(t, dest), want)
}

func TestWeRead7zSplitArchive(t *testing.T) {
	sevenZip := requireTool(t, "7z")
	work := t.TempDir()
	data := pseudoRandom(300_000, 11)
	if err := os.WriteFile(filepath.Join(work, "big.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	runTool(t, work, sevenZip, "a", "-tzip", "-mx0", "-v64k", "theirs.zip", "big.bin")
	if !exists(filepath.Join(work, "theirs.zip.002")) {
		t.Fatalf("7z did not create split volumes: %v", dirNames(t, work))
	}

	arch := filepath.Join(work, "theirs.zip")
	mustRun(t, exitOK, "t", arch)
	dest := filepath.Join(work, "dest")
	mustRun(t, exitOK, "x", "-o"+dest, arch+".001")
	got, err := os.ReadFile(filepath.Join(dest, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("content extracted from the 7z split set differs")
	}
}

func TestUnzipAcceptsOurArchives(t *testing.T) {
	unzip := requireTool(t, "unzip")
	src := makeSource(t)
	chdir(t, src)
	work := t.TempDir()

	single := filepath.Join(work, "ours.zip")
	mustRun(t, exitOK, "a", single, ".")
	runTool(t, work, unzip, "-tq", single)

	// 분할 볼륨은 이어 붙이면 unzip 이 그대로 읽을 수 있어야 한다.
	split := filepath.Join(work, "parts.zip")
	res, err := createArchive(addOptions{archive: split, inputs: []string{"."}, volumeSize: 3000, level: 6},
		new(bytes.Buffer), new(bytes.Buffer))
	if err != nil {
		t.Fatalf("createArchive: %v", err)
	}
	if len(res.volumes) < 2 {
		t.Fatalf("expected several volumes, got %v", res.volumes)
	}
	var joined []byte
	for _, v := range res.volumes {
		b, err := os.ReadFile(v)
		if err != nil {
			t.Fatal(err)
		}
		joined = append(joined, b...)
	}
	joinedPath := filepath.Join(work, "joined.zip")
	if err := os.WriteFile(joinedPath, joined, 0o644); err != nil {
		t.Fatal(err)
	}
	runTool(t, work, unzip, "-tq", joinedPath)
}
