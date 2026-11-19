package main

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestParseVolumeSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"65536", 64 << 10}, // 최소값 경계
		{"65536b", 64 << 10},
		{"64k", 64 << 10},
		{"64K", 64 << 10},
		{"64.5k", 66048},
		{"1440k", 1440 << 10},
		{"100m", 100 << 20},
		{"100MB", 100 << 20},
		{"100MiB", 100 << 20},
		{"1.5m", 3 << 19},
		{"1g", 1 << 30},
		{"2t", 2 << 40},
		{"9223372036854775807", math.MaxInt64}, // int64 상한 경계
	}
	for _, tc := range tests {
		got, err := parseVolumeSize(tc.in)
		if err != nil {
			t.Errorf("parseVolumeSize(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseVolumeSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseVolumeSizeRejects(t *testing.T) {
	bad := []string{
		"", "0", "0k",
		"65535", "63k", // 최소(64K) 바로 아래
		"-1m", "+1m",
		"abc", "k", ".", ".k", "1x", "1ib", "1kk",
		"1.2.3m", " 1m", "1m ", "1e3k", "0x10000", "NaN", "Inf",
		"9223372036854775808", // int64 상한 + 1
		"9999999999g", "8388608t", "9999999999.5g",
	}
	for _, in := range bad {
		if got, err := parseVolumeSize(in); err == nil {
			t.Errorf("parseVolumeSize(%q) = %d, want error", in, got)
		}
	}
}

func TestVolumeName(t *testing.T) {
	tests := []struct {
		index int
		want  string
	}{
		{1, "a.zip.001"},
		{12, "a.zip.012"},
		{999, "a.zip.999"},
		{1000, "a.zip.1000"}, // 세 자리를 넘으면 자릿수가 늘어난다(7-Zip과 동일)
	}
	for _, tc := range tests {
		if got := volumeName("a.zip", tc.index); got != tc.want {
			t.Errorf("volumeName(a.zip, %d) = %q, want %q", tc.index, got, tc.want)
		}
	}
}

func TestSplitVolumeSuffix(t *testing.T) {
	tests := []struct {
		in       string
		wantBase string
		wantOK   bool
	}{
		{"a.zip.001", "a.zip", true},
		{"a.zip.123", "a.zip", true},
		{"a.zip.1000", "a.zip", true},
		{filepath.Join("dir", "a.zip.002"), filepath.Join("dir", "a.zip"), true},
		{"a.zip", "a.zip", false},
		{"a.01", "a.01", false}, // 두 자리는 볼륨이 아님
		{"a.zip.00a", "a.zip.00a", false},
		{".001", ".001", false}, // 접미사만 있고 이름이 없음
		{"001", "001", false},
		{filepath.Join("dir.001", "a.zip"), filepath.Join("dir.001", "a.zip"), false},
		{"", "", false},
	}
	for _, tc := range tests {
		base, ok := splitVolumeSuffix(tc.in)
		if base != tc.wantBase || ok != tc.wantOK {
			t.Errorf("splitVolumeSuffix(%q) = (%q, %v), want (%q, %v)", tc.in, base, ok, tc.wantBase, tc.wantOK)
		}
	}
}

func TestSplitWriterBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		limit  int64
		chunks []int   // Write 호출별 바이트 수
		want   []int64 // 볼륨별 기대 크기
	}{
		{"no writes", 10, nil, nil},
		{"only empty writes", 10, []int{0, 0}, nil},
		{"one byte", 10, []int{1}, []int64{1}},
		{"exactly N", 10, []int{10}, []int64{10}},
		{"N plus one", 10, []int{11}, []int64{10, 1}},
		{"exactly 2N in small writes", 10, []int{5, 5, 5, 5}, []int64{10, 10}},
		{"one write spanning four volumes", 10, []int{35}, []int64{10, 10, 10, 5}},
		{"uneven writes", 10, []int{7, 7, 7}, []int64{10, 10, 1}},
		{"empty write between data", 10, []int{10, 0, 1}, []int64{10, 1}},
		{"limit of one byte", 1, []int{3}, []int64{1, 1, 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			base := filepath.Join(dir, "out.bin")
			w := newSplitWriter(func(i int) string { return volumeName(base, i) }, tc.limit)

			var all []byte
			next := byte(0)
			for _, n := range tc.chunks {
				chunk := make([]byte, n)
				for i := range chunk {
					chunk[i] = next
					next++
				}
				got, err := w.Write(chunk)
				if err != nil || got != n {
					t.Fatalf("Write(%d bytes) = %d, %v", n, got, err)
				}
				all = append(all, chunk...)
			}
			vols, err := w.Commit()
			if err != nil {
				t.Fatalf("Commit: %v", err)
			}
			if len(vols) != len(tc.want) {
				t.Fatalf("got %d volumes %v, want %d", len(vols), vols, len(tc.want))
			}
			if w.Written() != int64(len(all)) {
				t.Errorf("Written() = %d, want %d", w.Written(), len(all))
			}
			var joined []byte
			for i, v := range vols {
				if want := volumeName(base, i+1); v != want {
					t.Errorf("volume %d name = %q, want %q", i+1, v, want)
				}
				b, err := os.ReadFile(v)
				if err != nil {
					t.Fatal(err)
				}
				if int64(len(b)) != tc.want[i] {
					t.Errorf("volume %d size = %d, want %d", i+1, len(b), tc.want[i])
				}
				joined = append(joined, b...)
			}
			if !bytes.Equal(joined, all) {
				t.Errorf("joined volumes differ from the written data")
			}
			// 임시(.part) 파일이나 빈 여분 볼륨이 남으면 안 된다.
			if names := dirNames(t, dir); len(names) != len(tc.want) {
				t.Errorf("directory has %v, want exactly %d volume(s)", names, len(tc.want))
			}
		})
	}
}

func TestSplitWriterUnlimited(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "single.zip")
	w := newSplitWriter(func(int) string { return target }, 0)
	data := pseudoRandom(100_000, 3)
	for off := 0; off < len(data); off += 30_000 {
		end := off + 30_000
		if end > len(data) {
			end = len(data)
		}
		if _, err := w.Write(data[off:end]); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	vols, err := w.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(vols) != 1 || vols[0] != target {
		t.Fatalf("volumes = %v, want [%s]", vols, target)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("content differs")
	}
	if names := dirNames(t, dir); len(names) != 1 {
		t.Errorf("directory has %v, want only single.zip", names)
	}
}

// 기록 도중에는 최종 이름이 보이지 않아야 한다. 그래야 실패해도 기존 아카이브가 살아남는다.
func TestSplitWriterStagesUntilCommit(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "out.bin")
	old := volumeName(base, 1)
	if err := os.WriteFile(old, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := newSplitWriter(func(i int) string { return volumeName(base, i) }, 4)
	if _, err := w.Write([]byte("abcdefghij")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if b, _ := os.ReadFile(old); string(b) != "previous" {
		t.Errorf("existing volume was touched before Commit: %q", b)
	}
	for i := 1; i <= 3; i++ {
		if !exists(volumeName(base, i) + partSuffix) {
			t.Errorf("staging file for volume %d is missing", i)
		}
	}
	if exists(volumeName(base, 2)) {
		t.Errorf("final volume 2 exists before Commit")
	}
	if _, err := w.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if b, _ := os.ReadFile(old); string(b) != "abcd" {
		t.Errorf("volume 1 after Commit = %q, want %q", b, "abcd")
	}
	want := []string{"out.bin.001", "out.bin.002", "out.bin.003"}
	if got := dirNames(t, dir); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("directory = %v, want %v", got, want)
	}
}

func TestSplitWriterAbort(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "out.bin")
	w := newSplitWriter(func(i int) string { return volumeName(base, i) }, 4)
	if _, err := w.Write([]byte("abcdefghij")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	w.Abort()
	if names := dirNames(t, dir); len(names) != 0 {
		t.Errorf("Abort left files behind: %v", names)
	}
	w.Abort() // 두 번 불러도 안전해야 한다(defer와 명시 호출이 겹칠 수 있음)
}

func TestFindVolumes(t *testing.T) {
	touch := func(t *testing.T, names ...string) string {
		t.Helper()
		dir := t.TempDir()
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return filepath.Join(dir, "a.zip")
	}
	join := func(vols []string) string { return strings.Join(vols, "|") }

	t.Run("none", func(t *testing.T) {
		base := touch(t, "a.zip", "a.zip.txt", "b.zip.001")
		if vols := findVolumes(base); len(vols) != 0 {
			t.Errorf("findVolumes = %v, want no volumes", vols)
		}
		if got := missingVolume(base, 0); got != "" {
			t.Errorf("missingVolume = %q, want none", got)
		}
	})

	t.Run("contiguous set ignores lookalikes", func(t *testing.T) {
		base := touch(t, "a.zip.003", "a.zip.001", "a.zip.002",
			"a.zip", "a.zip.01", "a.zip.00a", "a.zip.0001", "a.zip.0004", "a.zip.001.part", "a.zipx.001")
		want := join([]string{base + ".001", base + ".002", base + ".003"})
		if got := join(findVolumes(base)); got != want {
			t.Errorf("findVolumes = %v, want %v", got, want)
		}
		// 정규 표기가 아닌 이름(.0004)은 뒤따르는 볼륨으로 치지 않는다.
		if got := missingVolume(base, 3); got != "" {
			t.Errorf("missingVolume = %q, want none for a complete set", got)
		}
	})

	// 번호가 끊기면 거기까지만 세트다. 끊긴 자리는 missingVolume 이 짚어 준다.
	t.Run("gap in the middle", func(t *testing.T) {
		base := touch(t, "a.zip.001", "a.zip.003")
		if got := join(findVolumes(base)); got != base+".001" {
			t.Errorf("findVolumes = %v, want only the first volume", got)
		}
		if got := missingVolume(base, 1); got != base+".002" {
			t.Errorf("missingVolume = %q, want %q", got, base+".002")
		}
	})

	t.Run("first volume missing", func(t *testing.T) {
		base := touch(t, "a.zip.002", "a.zip.003")
		if vols := findVolumes(base); len(vols) != 0 {
			t.Errorf("findVolumes = %v, want no volumes", vols)
		}
		if got := missingVolume(base, 0); got != base+".001" {
			t.Errorf("missingVolume = %q, want %q", got, base+".001")
		}
	})

	// 날짜를 붙인 사본처럼 번호가 동떨어진 파일은 세트의 일부가 아니다.
	t.Run("unrelated numbered file is not a volume", func(t *testing.T) {
		base := touch(t, "a.zip.001", "a.zip.002", "a.zip.20261119")
		want := join([]string{base + ".001", base + ".002"})
		if got := join(findVolumes(base)); got != want {
			t.Errorf("findVolumes = %v, want %v", got, want)
		}
	})

	t.Run("from an offset", func(t *testing.T) {
		base := touch(t, "a.zip.001", "a.zip.002", "a.zip.003", "a.zip.005")
		for from, want := range map[int]string{
			2: join([]string{base + ".002", base + ".003"}),
			4: "",
			5: base + ".005",
			6: "",
		} {
			if got := join(volumesFrom(base, from)); got != want {
				t.Errorf("volumesFrom(%d) = %q, want %q", from, got, want)
			}
		}
	})

	// 문자열 정렬이면 .1000 이 .999 앞에 오므로 번호 순서를 따로 검증한다.
	t.Run("more than 999 volumes stay in numeric order", func(t *testing.T) {
		dir := t.TempDir()
		base := filepath.Join(dir, "a.zip")
		const n = 1001
		for i := 1; i <= n; i++ {
			if err := os.WriteFile(volumeName(base, i), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		vols := findVolumes(base)
		if len(vols) != n {
			t.Fatalf("got %d volumes, want %d", len(vols), n)
		}
		for i, v := range vols {
			if want := volumeName(base, i+1); v != want {
				t.Fatalf("volume[%d] = %q, want %q", i, v, want)
			}
		}
	})
}

func TestParseVolumeName(t *testing.T) {
	tests := []struct {
		in        string
		wantBase  string
		wantIndex int
		wantOK    bool
	}{
		{"a.zip.001", "a.zip", 1, true},
		{"a.zip.012", "a.zip", 12, true},
		{"a.zip.1000", "a.zip", 1000, true},
		{"data.2024", "data", 2024, true},
		{"a.zip.000", "a.zip.000", 0, false},   // 볼륨 번호는 1부터
		{"a.zip.0012", "a.zip.0012", 0, false}, // 정규 표기가 아님(앞에 0 이 남음)
		{"a.zip.12", "a.zip.12", 0, false},
		{"a.zip", "a.zip", 0, false},
		{"a.zip.99999999999999999999", "a.zip.99999999999999999999", 0, false}, // int 범위 밖
	}
	for _, tc := range tests {
		base, index, ok := parseVolumeName(tc.in)
		if base != tc.wantBase || index != tc.wantIndex || ok != tc.wantOK {
			t.Errorf("parseVolumeName(%q) = (%q, %d, %v), want (%q, %d, %v)",
				tc.in, base, index, ok, tc.wantBase, tc.wantIndex, tc.wantOK)
		}
	}
}

func writeParts(t *testing.T, parts ...string) []string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "v.bin")
	paths := make([]string, 0, len(parts))
	for i, p := range parts {
		name := volumeName(base, i+1)
		if err := os.WriteFile(name, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, name)
	}
	return paths
}

func TestMultiReaderAt(t *testing.T) {
	// 크기 0인 볼륨을 끼워 경계 계산이 빈 볼륨을 건너뛰는지 본다.
	paths := writeParts(t, "abc", "", "defgh", "i")
	m, err := openMultiReaderAt(paths)
	if err != nil {
		t.Fatalf("openMultiReaderAt: %v", err)
	}
	defer m.Close()

	const whole = "abcdefghi"
	if m.Size() != int64(len(whole)) {
		t.Fatalf("Size() = %d, want %d", m.Size(), len(whole))
	}
	tests := []struct {
		off     int64
		n       int
		want    string
		wantEOF bool
	}{
		{0, 9, whole, false},
		{0, 3, "abc", false},
		{2, 3, "cde", false}, // 볼륨 1 → (빈 볼륨 2) → 볼륨 3
		{3, 5, "defgh", false},
		{7, 2, "hi", false},
		{4, 0, "", false},
		{7, 5, "hi", true}, // 끝을 넘으면 읽은 만큼과 EOF
		{9, 1, "", true},   // 정확히 끝
		{100, 1, "", true},
	}
	for _, tc := range tests {
		buf := make([]byte, tc.n)
		n, err := m.ReadAt(buf, tc.off)
		if got := string(buf[:n]); got != tc.want {
			t.Errorf("ReadAt(len %d, off %d) read %q, want %q", tc.n, tc.off, got, tc.want)
		}
		if tc.wantEOF && err != io.EOF {
			t.Errorf("ReadAt(len %d, off %d) err = %v, want io.EOF", tc.n, tc.off, err)
		}
		// io.ReaderAt 계약상 버퍼를 다 채운 경우 nil과 EOF 둘 다 허용된다.
		if !tc.wantEOF && err != nil && err != io.EOF {
			t.Errorf("ReadAt(len %d, off %d) err = %v", tc.n, tc.off, err)
		}
	}
	if n, err := m.ReadAt(make([]byte, 1), -1); err == nil || err == io.EOF || n != 0 {
		t.Errorf("ReadAt at negative offset = %d, %v; want a non-EOF error", n, err)
	}
}

// 볼륨 수가 동시에 열어 두는 파일 수보다 많아도(핸들 재활용) 내용이 맞아야 한다.
func TestMultiReaderAtManyVolumes(t *testing.T) {
	const n = 40
	parts := make([]string, n)
	want := make([]byte, n)
	for i := range parts {
		want[i] = byte('A' + i%26)
		parts[i] = string(want[i : i+1])
	}
	m, err := openMultiReaderAt(writeParts(t, parts...))
	if err != nil {
		t.Fatalf("openMultiReaderAt: %v", err)
	}
	defer m.Close()

	buf := make([]byte, n)
	if got, err := m.ReadAt(buf, 0); got != n || (err != nil && err != io.EOF) {
		t.Fatalf("ReadAt whole = %d, %v", got, err)
	}
	if !bytes.Equal(buf, want) {
		t.Errorf("whole read = %q, want %q", buf, want)
	}
	// 뒤에서 앞으로 한 바이트씩: 매번 다른 볼륨을 열어야 한다.
	one := make([]byte, 1)
	for i := n - 1; i >= 0; i-- {
		if _, err := m.ReadAt(one, int64(i)); err != nil && err != io.EOF {
			t.Fatalf("ReadAt(off %d): %v", i, err)
		}
		if one[0] != want[i] {
			t.Fatalf("byte %d = %q, want %q", i, one[0], want[i])
		}
	}
}

func TestOpenMultiReaderAtMissingFile(t *testing.T) {
	paths := writeParts(t, "abc", "def")
	if err := os.Remove(paths[1]); err != nil {
		t.Fatal(err)
	}
	if m, err := openMultiReaderAt(paths); err == nil {
		m.Close()
		t.Errorf("openMultiReaderAt succeeded with a missing volume")
	}
	if m, err := openMultiReaderAt(nil); err == nil {
		m.Close()
		t.Errorf("openMultiReaderAt(nil) succeeded")
	}
}

// io.ReaderAt 계약은 동시 호출을 허용한다. 볼륨 핸들이 계속 교체되는 와중에도 내용이 맞아야 한다.
func TestMultiReaderAtConcurrent(t *testing.T) {
	const vols, volSize = 40, 7 // 열어 둘 수 있는 수(8)보다 훨씬 많은 볼륨
	whole := pseudoRandom(vols*volSize, 9)
	parts := make([]string, vols)
	for i := range parts {
		parts[i] = string(whole[i*volSize : (i+1)*volSize])
	}
	m, err := openMultiReaderAt(writeParts(t, parts...))
	if err != nil {
		t.Fatalf("openMultiReaderAt: %v", err)
	}
	defer m.Close()

	const readers = 8
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for g := 0; g < readers; g++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			buf := make([]byte, 3*volSize) // 한 번에 볼륨 서너 개를 걸치는 읽기
			for i := 0; i < 100; i++ {
				off := rng.Intn(len(whole))
				n := 1 + rng.Intn(len(buf))
				if off+n > len(whole) {
					n = len(whole) - off
				}
				got, err := m.ReadAt(buf[:n], int64(off))
				if got != n || (err != nil && err != io.EOF) || !bytes.Equal(buf[:n], whole[off:off+n]) {
					errs <- fmt.Errorf("ReadAt(len %d, off %d) = %d, %v: wrong result", n, off, got, err)
					return
				}
			}
		}(int64(g))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
