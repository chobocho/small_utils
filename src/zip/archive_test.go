package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEntryName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a.txt", "a.txt"},
		{"./a.txt", "a.txt"},
		{"dir/sub/", "dir/sub"},
		{"a//b", "a/b"},
		{"a/../b", "b"},
		{"/abs/x", "abs/x"},  // 절대 경로는 상대 경로로 저장
		{"../../x/y", "x/y"}, // 상위 탈출 성분은 버린다
		{".", ""},            // 현재 폴더 자체는 엔트리가 아니다
		{"..", ""},
		{"../..", ""},
		{"/", ""},
	}
	for _, tc := range tests {
		if got := entryName(filepath.FromSlash(tc.in)); got != tc.want {
			t.Errorf("entryName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// createArchive를 직접 불러 CLI 최소값(64K)보다 작은 볼륨으로 경계를 촘촘히 지나게 한다.
func TestRoundTripAcrossVolumeSizes(t *testing.T) {
	src := makeSource(t)
	want := readTree(t, src)
	chdir(t, src)

	for _, level := range []int{0, 6, 9} {
		for _, vol := range []int64{0, 700, 4096, 1 << 20} {
			t.Run(fmt.Sprintf("m%d_v%d", level, vol), func(t *testing.T) {
				work := t.TempDir()
				arch := filepath.Join(work, "out.zip")
				res, err := createArchive(addOptions{
					archive: arch, inputs: []string{"."}, volumeSize: vol, level: level,
				}, io.Discard, io.Discard)
				if err != nil {
					t.Fatalf("createArchive: %v", err)
				}
				if res.files != sampleFiles || res.dirs != sampleDirs || res.warnings != 0 {
					t.Errorf("result = %d files, %d dirs, %d warnings; want %d, %d, 0",
						res.files, res.dirs, res.warnings, sampleFiles, sampleDirs)
				}
				if res.inBytes != sampleBytes {
					t.Errorf("inBytes = %d, want %d", res.inBytes, sampleBytes)
				}

				var total int64
				for i, v := range res.volumes {
					st, err := os.Stat(v)
					if err != nil {
						t.Fatal(err)
					}
					total += st.Size()
					if vol == 0 {
						continue
					}
					if want := volumeName(arch, i+1); v != want {
						t.Errorf("volume %d = %q, want %q", i+1, v, want)
					}
					last := i == len(res.volumes)-1
					if !last && st.Size() != vol {
						t.Errorf("volume %d size = %d, want exactly %d", i+1, st.Size(), vol)
					}
					if last && (st.Size() < 1 || st.Size() > vol) {
						t.Errorf("last volume size = %d, want 1..%d", st.Size(), vol)
					}
				}
				if vol == 0 && (len(res.volumes) != 1 || res.volumes[0] != arch) {
					t.Errorf("volumes = %v, want [%s]", res.volumes, arch)
				}
				if res.outBytes != total {
					t.Errorf("outBytes = %d, but the volumes hold %d bytes", res.outBytes, total)
				}
				if names := dirNames(t, work); len(names) != len(res.volumes) {
					t.Errorf("work dir has %v, want only the %d volume(s)", names, len(res.volumes))
				}

				mustRun(t, exitOK, "t", arch)
				dest := filepath.Join(work, "dest")
				mustRun(t, exitOK, "x", "-o"+dest, arch)
				assertTreeEqual(t, readTree(t, dest), want)
			})
		}
	}
}

// 볼륨이 999개를 넘으면 접미사 자릿수가 늘어난다. 쓰기·찾기·읽기가 모두 번호순이어야 한다.
func TestManyVolumesBeyond999(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	data := pseudoRandom(17000, 7)
	if err := os.WriteFile("big.bin", data, 0o644); err != nil {
		t.Fatal(err)
	}
	arch := filepath.Join(t.TempDir(), "many.zip")
	res, err := createArchive(addOptions{archive: arch, inputs: []string{"big.bin"}, volumeSize: 16, level: 0},
		io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("createArchive: %v", err)
	}
	if len(res.volumes) <= 1000 {
		t.Fatalf("got %d volumes, the test needs more than 1000", len(res.volumes))
	}
	if !exists(arch + ".1000") {
		t.Errorf("volume %s.1000 was not created", arch)
	}
	mustRun(t, exitOK, "t", arch)
	dest := filepath.Join(work, "dest")
	mustRun(t, exitOK, "x", "-o"+dest, arch+".001")
	got, err := os.ReadFile(filepath.Join(dest, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("extracted content differs")
	}
}

// makeSplit: 무압축 300KB 한 파일을 64K 볼륨 5개로 나눈 아카이브를 만든다.
// 무압축이라 볼륨 2~4는 전부 파일 데이터이고, 한 바이트만 바꿔도 CRC가 어긋난다.
func makeSplit(t *testing.T) (work string, data []byte) {
	t.Helper()
	work = t.TempDir()
	chdir(t, work)
	data = pseudoRandom(300_000, 42)
	if err := os.WriteFile("big.bin", data, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, exitOK, "a", "-v64k", "-m0", "big.zip", "big.bin")
	return work, data
}

func TestRunSplitArchive(t *testing.T) {
	_, data := makeSplit(t)

	want := []string{"big.bin", "big.zip.001", "big.zip.002", "big.zip.003", "big.zip.004", "big.zip.005"}
	if got := dirNames(t, "."); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("directory = %v, want %v", got, want)
	}
	for i := 1; i <= 5; i++ {
		st, err := os.Stat(volumeName("big.zip", i))
		if err != nil {
			t.Fatal(err)
		}
		if i < 5 && st.Size() != 64<<10 {
			t.Errorf("volume %d size = %d, want %d", i, st.Size(), 64<<10)
		}
		if i == 5 && (st.Size() < 1 || st.Size() > 64<<10) {
			t.Errorf("last volume size = %d", st.Size())
		}
	}

	// 아카이브 이름은 기본 이름, 아무 볼륨, 확장자 생략 어느 쪽으로 줘도 같은 세트를 연다.
	for _, name := range []string{"big.zip", "big.zip.001", "big.zip.003", "big"} {
		mustRun(t, exitOK, "t", name)
	}

	mustRun(t, exitOK, "x", "-oout", "big.zip")
	got, err := os.ReadFile(filepath.Join("out", "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("extracted content differs")
	}

	// 도움말이 약속하는 `cat big.zip.* > big.zip`: 이어 붙이면 표준 ZIP이어야 한다.
	var joined []byte
	for i := 1; i <= 5; i++ {
		b, err := os.ReadFile(volumeName("big.zip", i))
		if err != nil {
			t.Fatal(err)
		}
		joined = append(joined, b...)
	}
	zr, err := zip.NewReader(bytes.NewReader(joined), int64(len(joined)))
	if err != nil {
		t.Fatalf("joined volumes are not a valid zip: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "big.bin" {
		t.Fatalf("joined zip entries = %v", zr.File)
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading the joined zip: %v", err)
	}
	if !bytes.Equal(b, data) {
		t.Errorf("joined zip content differs")
	}
}

func TestRunAddExtractRoundTrip(t *testing.T) {
	src := makeSource(t)
	want := readTree(t, src)
	chdir(t, src)
	work := t.TempDir()
	arch := filepath.Join(work, "plain.zip")

	stdout, _ := mustRun(t, exitOK, "a", arch, ".")
	if !strings.Contains(stdout, "hello.txt") || !strings.Contains(stdout, "5 file(s)") {
		t.Errorf("add output should name the files and the count:\n%s", stdout)
	}
	if got := dirNames(t, work); len(got) != 1 || got[0] != "plain.zip" {
		t.Fatalf("work dir = %v, want only plain.zip", got)
	}
	dest := filepath.Join(work, "dest")
	mustRun(t, exitOK, "x", "-o"+dest, arch)
	assertTreeEqual(t, readTree(t, dest), want)
}

// 최상위 폴더 이름을 인자로 주면 그 이름이 경로에 남아야 한다(zip -r 과 같은 동작).
func TestRunAddNamedDirectoriesAndFiles(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	writeTree(t, work, map[string]string{
		"docs/a.txt":     "A",
		"docs/sub/b.txt": "B",
		"docs/empty/":    "",
		"top.txt":        "T",
		"other/skip.txt": "S",
	})
	// docs 를 두 번 주고 그 안의 파일도 다시 줘도 엔트리는 한 번씩만 들어가야 한다.
	mustRun(t, exitOK, "a", "sel.zip", "docs", "top.txt", "docs/", filepath.Join("docs", "a.txt"))
	want := []string{"docs/", "docs/a.txt", "docs/empty/", "docs/sub/", "docs/sub/b.txt", "top.txt"}
	if got := archiveNames(t, "sel.zip"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("entries = %v\n   want %v", got, want)
	}
}

func TestRunAddRefusesExistingArchive(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	writeTree(t, work, map[string]string{"one.txt": "1", "two.txt": "2"})

	mustRun(t, exitOK, "a", "arc.zip", "one.txt")
	before, err := os.ReadFile("arc.zip")
	if err != nil {
		t.Fatal(err)
	}
	_, stderr := mustRun(t, exitError, "a", "arc.zip", "two.txt")
	if !strings.Contains(stderr, "arc.zip") || !strings.Contains(stderr, "-y") {
		t.Errorf("stderr should name the archive and suggest -y: %q", stderr)
	}
	after, _ := os.ReadFile("arc.zip")
	if !bytes.Equal(before, after) {
		t.Errorf("existing archive was modified without -y")
	}
	// 분할 모드로 바꿔도 같은 이름의 단일 아카이브가 있으면 거부해야 한다.
	mustRun(t, exitError, "a", "-v64k", "arc.zip", "two.txt")
	if exists("arc.zip.001") || exists("arc.zip.001"+partSuffix) {
		t.Errorf("refused run left volume files behind: %v", dirNames(t, "."))
	}

	mustRun(t, exitOK, "a", "-y", "arc.zip", "two.txt")
	if got := archiveNames(t, "arc.zip"); len(got) != 1 || got[0] != "two.txt" {
		t.Errorf("entries after -y = %v, want [two.txt]", got)
	}
}

// 큰 세트를 작은 세트로 덮어쓸 때 옛 볼륨이 남으면 다음 읽기가 깨진다.
func TestRunAddOverwriteRemovesStaleFiles(t *testing.T) {
	work, _ := makeSplit(t) // big.zip.001 ~ .005
	if err := os.WriteFile("small.bin", pseudoRandom(70_000, 5), 0o644); err != nil {
		t.Fatal(err)
	}

	mustRun(t, exitError, "a", "-v64k", "-m0", "big.zip", "small.bin")
	if !exists("big.zip.005") {
		t.Fatalf("refused run removed existing volumes: %v", dirNames(t, work))
	}

	mustRun(t, exitOK, "a", "-y", "-v64k", "-m0", "big.zip", "small.bin")
	want := []string{"big.bin", "big.zip.001", "big.zip.002", "small.bin"}
	if got := dirNames(t, work); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("after shrinking: directory = %v, want %v", got, want)
	}
	mustRun(t, exitOK, "t", "big.zip")

	// 분할 → 단일: 볼륨은 사라지고 big.zip 하나만 남는다.
	mustRun(t, exitOK, "a", "-y", "-m0", "big.zip", "small.bin")
	want = []string{"big.bin", "big.zip", "small.bin"}
	if got := dirNames(t, work); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("split → single: directory = %v, want %v", got, want)
	}
	mustRun(t, exitOK, "t", "big.zip")

	// 단일 → 분할: big.zip 은 사라지고 볼륨만 남는다.
	mustRun(t, exitOK, "a", "-y", "-v64k", "-m0", "big.zip", "small.bin")
	want = []string{"big.bin", "big.zip.001", "big.zip.002", "small.bin"}
	if got := dirNames(t, work); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("single → split: directory = %v, want %v", got, want)
	}
	mustRun(t, exitOK, "t", "big.zip")
}

// 현재 폴더를 통째로 넣을 때 만들고 있는(또는 덮어쓸) 아카이브 자신은 빠져야 한다.
func TestRunAddSkipsOwnOutput(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	writeTree(t, work, map[string]string{"a.txt": strings.Repeat("x", 1000)})

	mustRun(t, exitOK, "a", "out.zip", ".")
	mustRun(t, exitOK, "a", "-y", "out.zip", ".")
	if got := archiveNames(t, "out.zip"); len(got) != 1 || got[0] != "a.txt" {
		t.Errorf("out.zip entries = %v, want [a.txt]", got)
	}

	// 다른 아카이브(out.zip)는 평범한 입력이고, 자기 볼륨만 빠져야 한다.
	for i := 0; i < 2; i++ {
		mustRun(t, exitOK, "a", "-y", "-v64k", "vol.zip", ".")
		if got := archiveNames(t, "vol.zip"); strings.Join(got, ",") != "a.txt,out.zip" {
			t.Errorf("run %d: vol.zip entries = %v, want [a.txt out.zip]", i+1, got)
		}
	}
}

func TestRunAddMissingInput(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	writeTree(t, work, map[string]string{"a.txt": "A"})

	// 일부만 없어도 아무것도 만들지 않는다(반쪽짜리 백업 방지).
	_, stderr := mustRun(t, exitError, "a", "arc.zip", "a.txt", "nope.txt")
	if !strings.Contains(stderr, "nope.txt") {
		t.Errorf("stderr should name the missing input: %q", stderr)
	}
	if got := dirNames(t, work); len(got) != 1 {
		t.Errorf("failed run left files behind: %v", got)
	}
}

func TestRunAddNothingToAdd(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	mustRun(t, exitError, "a", "arc.zip", ".")
	if got := dirNames(t, work); len(got) != 0 {
		t.Errorf("failed run left files behind: %v", got)
	}
}

func TestRunList(t *testing.T) {
	src := makeSource(t)
	chdir(t, src)
	arch := filepath.Join(t.TempDir(), "a.zip")
	mustRun(t, exitOK, "a", arch, ".")

	stdout, _ := mustRun(t, exitOK, "l", arch)
	for _, want := range []string{
		"hello.txt", "한글 폴더/한글파일.txt", "deep/a/b/data.bin", "emptydir/",
		"5 file(s)", fmt.Sprint(sampleBytes),
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("listing is missing %q:\n%s", want, stdout)
		}
	}

	stdout, _ = mustRun(t, exitOK, "l", arch, "*.bin")
	if !strings.Contains(stdout, "data.bin") || strings.Contains(stdout, "hello.txt") || !strings.Contains(stdout, "1 file(s)") {
		t.Errorf("filtered listing is wrong:\n%s", stdout)
	}

	// 아무것도 안 걸리면 조용히 성공하지 말고 경고로 알린다.
	mustRun(t, exitWarn, "l", arch, "*.nomatch")
}

func TestRunCorruptVolume(t *testing.T) {
	makeSplit(t)
	vol := volumeName("big.zip", 2)
	b, err := os.ReadFile(vol)
	if err != nil {
		t.Fatal(err)
	}
	b[100] ^= 0xFF
	if err := os.WriteFile(vol, b, 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr := mustRun(t, exitError, "t", "big.zip")
	if !strings.Contains(stderr, "big.bin") {
		t.Errorf("stderr should name the damaged entry: %q", stderr)
	}

	// 손상된 내용을 정상 파일처럼 남기면 안 된다.
	mustRun(t, exitError, "x", "-oout", "big.zip")
	if exists(filepath.Join("out", "big.bin")) {
		t.Errorf("corrupt file was left in the output directory")
	}
	if exists("out") {
		if names := dirNames(t, "out"); len(names) != 0 {
			t.Errorf("output directory has leftovers: %v", names)
		}
	}
}

func TestRunMissingVolume(t *testing.T) {
	t.Run("middle", func(t *testing.T) {
		makeSplit(t)
		if err := os.Remove(volumeName("big.zip", 2)); err != nil {
			t.Fatal(err)
		}
		for _, cmd := range []string{"t", "l", "x"} {
			_, stderr := mustRun(t, exitError, cmd, "big.zip")
			if !strings.Contains(stderr, "big.zip.002") || !strings.Contains(stderr, "missing") {
				t.Errorf("%s: stderr should say big.zip.002 is missing: %q", cmd, stderr)
			}
		}
		// big.bin + 남은 볼륨 4개 그대로여야 한다(실패한 해제가 임시 파일을 남기지 않음).
		if got := dirNames(t, "."); len(got) != 5 {
			t.Errorf("failed extract left files behind: %v", got)
		}
	})
	t.Run("last", func(t *testing.T) {
		makeSplit(t)
		if err := os.Remove(volumeName("big.zip", 5)); err != nil {
			t.Fatal(err)
		}
		// 마지막 볼륨에 중앙 디렉터리가 있어 번호만으로는 누락을 알 수 없다.
		// 그래도 성공으로 끝나면 안 되고, 볼륨 문제일 수 있다는 힌트를 줘야 한다.
		_, stderr := mustRun(t, exitError, "t", "big.zip")
		if !strings.Contains(stderr, "volume") {
			t.Errorf("stderr should hint at a missing volume: %q", stderr)
		}
	})
}

func TestRunAddSymlinks(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	writeTree(t, work, map[string]string{
		"src/real.txt":    "real",
		"src/sub/in.txt":  "in",
		"outside/far.txt": "far",
	})
	link := func(target, name string) {
		t.Helper()
		if err := os.Symlink(target, filepath.FromSlash(name)); err != nil {
			t.Skipf("symlinks are not available here: %v", err)
		}
	}
	link("real.txt", "src/file-link.txt")
	link(filepath.Join("..", "outside"), "src/dir-link")
	link("nowhere", "src/broken-link")
	link("..", "src/sub/loop") // 조상을 가리키는 순환: 따라가면 끝나지 않는다

	_, stderr := mustRun(t, exitWarn, "a", "links.zip", "src")
	for _, want := range []string{"dir-link", "broken-link", "loop"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr should mention the skipped %s: %q", want, stderr)
		}
	}
	want := "src/|src/file-link.txt|src/real.txt|src/sub/|src/sub/in.txt"
	if got := strings.Join(archiveNames(t, "links.zip"), "|"); got != want {
		t.Errorf("entries = %s\n   want %s", got, want)
	}

	// 파일 링크는 링크가 아니라 가리키는 내용으로 저장된다.
	mustRun(t, exitOK, "x", "-oout", "links.zip")
	st, err := os.Lstat(filepath.Join("out", "src", "file-link.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Mode().IsRegular() {
		t.Errorf("file-link.txt was restored as %v, want a regular file", st.Mode())
	}
	if b, _ := os.ReadFile(filepath.Join("out", "src", "file-link.txt")); string(b) != "real" {
		t.Errorf("file-link.txt content = %q, want %q", b, "real")
	}

	// 사용자가 인자로 직접 준 폴더 링크는 의도가 분명하므로 따라간다.
	mustRun(t, exitOK, "a", "top.zip", filepath.Join("src", "dir-link"))
	want = "src/dir-link/|src/dir-link/far.txt"
	if got := strings.Join(archiveNames(t, "top.zip"), "|"); got != want {
		t.Errorf("entries = %s\n   want %s", got, want)
	}
}

// 읽을 수 없는 파일 하나 때문에 전체 백업이 실패하면 안 된다: 건너뛰고 경고로 알린다.
func TestRunAddUnreadableFile(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	writeTree(t, work, map[string]string{"ok.txt": "ok", "locked.txt": "secret", "z.txt": "z"})
	if err := os.Chmod("locked.txt", 0); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open("locked.txt"); err == nil {
		f.Close()
		t.Skip("file permissions are not enforced for this user")
	}

	_, stderr := mustRun(t, exitWarn, "a", "p.zip", "ok.txt", "locked.txt", "z.txt")
	if !strings.Contains(stderr, "locked.txt") {
		t.Errorf("stderr should name the unreadable file: %q", stderr)
	}
	// 건너뛴 파일은 헤더조차 남지 않아야 하고, 뒤따르는 파일은 정상이어야 한다.
	if got := strings.Join(archiveNames(t, "p.zip"), "|"); got != "ok.txt|z.txt" {
		t.Errorf("entries = %s, want ok.txt|z.txt", got)
	}
	mustRun(t, exitOK, "t", "p.zip")
}

// 아카이브 옆에 숫자 확장자를 가진 남의 파일(날짜를 붙인 사본 등)이 있을 수 있다.
// 볼륨처럼 생겼다는 이유로 지우거나, 그 때문에 정상 아카이브를 못 읽으면 안 된다.
func TestRunLeavesUnrelatedNumberedFilesAlone(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	if err := os.WriteFile("small.bin", pseudoRandom(70_000, 5), 0o644); err != nil {
		t.Fatal(err)
	}
	const dated = "arc.zip.20261119"
	if err := os.WriteFile(dated, []byte("my dated copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	intact := func(step string) {
		t.Helper()
		if b, err := os.ReadFile(dated); err != nil || string(b) != "my dated copy" {
			t.Fatalf("%s: unrelated file %s was damaged: %q, %v", step, dated, b, err)
		}
	}
	wantDir := func(step string, want ...string) {
		t.Helper()
		if got := dirNames(t, work); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: directory = %v, want %v", step, got, want)
		}
	}

	mustRun(t, exitOK, "a", "-m0", "arc.zip", "small.bin")
	intact("create single")
	mustRun(t, exitOK, "t", "arc.zip")

	mustRun(t, exitOK, "a", "-y", "-m0", "arc.zip", "small.bin")
	intact("overwrite single")

	mustRun(t, exitOK, "a", "-y", "-v64k", "-m0", "arc.zip", "small.bin")
	intact("single → split")
	wantDir("single → split", "arc.zip.001", "arc.zip.002", dated, "small.bin")
	// 완전한 세트 뒤에 번호가 동떨어진 파일이 있어도 "볼륨 누락" 으로 오해하지 않는다.
	for _, name := range []string{"arc.zip", "arc.zip.002"} {
		mustRun(t, exitOK, "t", name)
	}

	mustRun(t, exitOK, "a", "-y", "-m0", "arc.zip", "small.bin")
	intact("split → single")
	wantDir("split → single", "arc.zip", dated, "small.bin")
}

// 폴더를 통째로 넣을 때도 그런 파일은 사용자의 데이터이므로 아카이브에 들어가야 한다.
func TestRunAddKeepsUnrelatedNumberedInputs(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	writeTree(t, work, map[string]string{"a.txt": "A", "all.zip.20261119": "dated copy"})
	mustRun(t, exitOK, "a", "all.zip", ".")
	if got := strings.Join(archiveNames(t, "all.zip"), "|"); got != "a.txt|all.zip.20261119" {
		t.Errorf("entries = %s, want a.txt|all.zip.20261119", got)
	}
}

// 숫자 확장자를 가진 평범한 ZIP 은 볼륨 조각이 아니라 그 자체로 열려야 한다.
func TestRunOpensZipWithNumericExtension(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	buildZip(t, "data.2024", []zipEntry{{name: "inside-2024.txt", body: "in"}})

	stdout, _ := mustRun(t, exitOK, "l", "data.2024")
	if !strings.Contains(stdout, "inside-2024.txt") {
		t.Errorf("listing of data.2024 is wrong:\n%s", stdout)
	}
	mustRun(t, exitOK, "t", "data.2024")

	// 확장자를 생략하면 data.zip 을 찾아야 한다. data.2024 를 "data" 세트의 볼륨으로 보면 안 된다.
	buildZip(t, "data.zip", []zipEntry{{name: "inside-plain.txt", body: "in"}})
	stdout, _ = mustRun(t, exitOK, "l", "data")
	if !strings.Contains(stdout, "inside-plain.txt") {
		t.Errorf("listing of data should come from data.zip:\n%s", stdout)
	}

	// 진짜 세트(data.001, data.002 …)가 옆에 있어도 사용자가 이름을 친 파일을 연다.
	if err := os.WriteFile("big.bin", pseudoRandom(5000, 8), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := createArchive(addOptions{archive: "data", inputs: []string{"big.bin"}, volumeSize: 2000, level: 0},
		io.Discard, io.Discard)
	if err != nil || len(res.volumes) < 2 {
		t.Fatalf("createArchive: %v (volumes %v)", err, res.volumes)
	}
	stdout, _ = mustRun(t, exitOK, "l", "data.2024")
	if !strings.Contains(stdout, "inside-2024.txt") {
		t.Errorf("data.2024 should still open as itself:\n%s", stdout)
	}
	stdout, _ = mustRun(t, exitOK, "l", "data.002")
	if !strings.Contains(stdout, "big.bin") {
		t.Errorf("data.002 should open the data.001… set:\n%s", stdout)
	}
}

// -y 없이는 번호 붙은 기존 파일을 덮어쓰거나 새 세트에 섞이게 두지 않는다.
func TestRunAddNumberedCollisions(t *testing.T) {
	work := t.TempDir()
	chdir(t, work)
	if err := os.WriteFile("small.bin", pseudoRandom(70_000, 5), 0o644); err != nil { // 볼륨 2개 분량
		t.Fatal(err)
	}
	wantDir := func(step string, want ...string) {
		t.Helper()
		if got := dirNames(t, work); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: directory = %v, want %v", step, got, want)
		}
	}

	// 새 볼륨이 쓰일 자리(.002)에 파일이 있다. 첫 볼륨(.001)은 없어서 "아카이브가 있다" 고는 할 수 없는 상태.
	if err := os.WriteFile("arc.zip.002", []byte("stray"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr := mustRun(t, exitError, "a", "-v64k", "-m0", "arc.zip", "small.bin")
	if !strings.Contains(stderr, "arc.zip.002") {
		t.Errorf("stderr should name the colliding file: %q", stderr)
	}
	if b, _ := os.ReadFile("arc.zip.002"); string(b) != "stray" {
		t.Fatalf("existing arc.zip.002 was overwritten without -y: %q", b)
	}
	wantDir("collision", "arc.zip.002", "small.bin")

	// 새 세트 바로 다음 번호(.003): 그대로 두면 세트의 세 번째 볼륨으로 읽혀 아카이브가 깨진다.
	if err := os.Rename("arc.zip.002", "arc.zip.003"); err != nil {
		t.Fatal(err)
	}
	_, stderr = mustRun(t, exitError, "a", "-v64k", "-m0", "arc.zip", "small.bin")
	if !strings.Contains(stderr, "arc.zip.003") {
		t.Errorf("stderr should name the trailing file: %q", stderr)
	}
	wantDir("trailing", "arc.zip.003", "small.bin")

	mustRun(t, exitOK, "a", "-y", "-v64k", "-m0", "arc.zip", "small.bin")
	wantDir("with -y", "arc.zip.001", "arc.zip.002", "small.bin")
	mustRun(t, exitOK, "t", "arc.zip")
}

// 가운데가 빠진 옛 세트를 덮어쓸 때, 새 세트 뒤로 이어지는 옛 볼륨이 남으면 안 된다.
func TestRunAddOverwritesBrokenSet(t *testing.T) {
	work, _ := makeSplit(t) // big.zip.001 ~ .005
	if err := os.Remove("big.zip.002"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("small.bin", pseudoRandom(70_000, 5), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, exitOK, "a", "-y", "-v64k", "-m0", "big.zip", "small.bin")
	want := []string{"big.bin", "big.zip.001", "big.zip.002", "small.bin"}
	if got := dirNames(t, work); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("directory = %v, want %v", got, want)
	}
	mustRun(t, exitOK, "t", "big.zip")
}
