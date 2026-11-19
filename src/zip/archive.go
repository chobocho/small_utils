package main

import (
	"archive/zip"
	"bufio"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type addOptions struct {
	archive    string // 최종 아카이브 경로(분할이면 볼륨 접미사를 뺀 이름)
	inputs     []string
	volumeSize int64 // 0 이면 나누지 않는다
	level      int   // 0 = 무압축, 1~9 = deflate 수준
	overwrite  bool
}

type addResult struct {
	files    int
	dirs     int
	warnings int
	volumes  []string
	inBytes  int64 // 원본 파일 크기 합
	outBytes int64 // 만들어진 아카이브(모든 볼륨) 크기 합
}

type addEntry struct {
	path string // 디스크 경로
	name string // 아카이브 안 이름('/' 구분, 디렉터리는 '/' 로 끝남)
	info fs.FileInfo
}

// entryName 은 사용자가 준 경로를 아카이브 안 이름으로 바꾼다.
// 절대 경로의 루트와 앞쪽 '..' 는 버려서, 풀 때 대상 폴더 밖을 가리키는 이름이 생기지 않게 한다.
// 현재 폴더 자체(".")처럼 이름이 남지 않으면 "" 를 돌려준다.
func entryName(p string) string {
	p = filepath.Clean(p)
	p = filepath.ToSlash(p[len(filepath.VolumeName(p)):])
	p = strings.TrimLeft(p, "/")
	// Clean 을 거친 상대 경로에서 '..' 는 맨 앞에만 남는다.
	for p == ".." || strings.HasPrefix(p, "../") {
		p = strings.TrimPrefix(strings.TrimPrefix(p, ".."), "/")
	}
	if p == "." {
		return ""
	}
	return p
}

// collector 는 기록을 시작하기 전에 넣을 항목을 전부 모은다.
// 먼저 모아야 입력이 하나라도 없을 때 아무것도 만들지 않고 끝낼 수 있다.
// O(N) 시간·공간: N 은 파일과 폴더 수.
type collector struct {
	entries  []addEntry
	seen     map[string]struct{} // 같은 경로를 두 번 줘도 엔트리는 하나만 만든다
	isOutput func(abs string) bool
	rep      *reporter
}

func (c *collector) addInput(p string) error {
	// 사용자가 직접 지정한 경로는 심링크여도 따라간다(명시적 의도).
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	c.walk(p, abs, entryName(p), info)
	return nil
}

// walk 는 abs 를 부모에서 이어 붙여 내려보낸다. 항목마다 filepath.Abs 를 부르면
// 매번 작업 폴더를 조회하는 시스템 콜이 붙어 파일이 많을 때 눈에 띄게 느려진다.
func (c *collector) walk(p, abs, name string, info fs.FileInfo) {
	// 만들고 있는(또는 덮어쓸) 아카이브 자신을 담으면 안 된다: `czip a out.zip .`
	if c.isOutput(abs) {
		return
	}
	switch {
	case info.IsDir():
		if name != "" {
			c.add(p, name+"/", info) // 폴더마다 엔트리를 남겨야 빈 폴더도 복원된다
		}
		ents, err := os.ReadDir(p)
		if err != nil {
			c.rep.warnf("%v", err)
			return
		}
		for _, e := range ents {
			child := filepath.Join(p, e.Name())
			ci, err := e.Info()
			if err == nil && ci.Mode()&fs.ModeSymlink != 0 {
				// 파일 링크는 내용을 담고, 폴더 링크는 따라가지 않는다(순환 방지).
				if ci, err = os.Stat(child); err == nil && ci.IsDir() {
					c.rep.warnf("%s: symbolic link to a directory, not followed", child)
					continue
				}
			}
			if err != nil {
				c.rep.warnf("%v", err)
				continue
			}
			c.walk(child, filepath.Join(abs, e.Name()), path.Join(name, e.Name()), ci)
		}
	case info.Mode().IsRegular():
		c.add(p, name, info)
	default:
		c.rep.warnf("%s: not a regular file, skipped", p)
	}
}

func (c *collector) add(p, name string, info fs.FileInfo) {
	if _, dup := c.seen[name]; dup {
		return
	}
	c.seen[name] = struct{}{}
	c.entries = append(c.entries, addEntry{path: p, name: name, info: info})
}

// outputMatcher 는 주어진 절대 경로가 base 아카이브 자신인지 판정한다: 이번 실행으로 교체될
// 옛 아카이브(단일 파일, .001 부터 이어지는 세트)와 czip 의 임시 파일이 해당한다.
// 번호 모양이라도 세트에 속하지 않는 파일(backup.zip.20261119)은 사용자의 데이터이므로 걸러내지 않는다.
func outputMatcher(base string) func(abs string) bool {
	absBase, err := filepath.Abs(base)
	if err != nil {
		absBase = base
	}
	old := map[string]struct{}{absBase: {}}
	for _, v := range findVolumes(absBase) {
		old[v] = struct{}{}
	}
	return func(abs string) bool {
		if _, ok := old[abs]; ok {
			return true
		}
		rest := strings.TrimSuffix(abs, partSuffix)
		if rest == abs {
			return false
		}
		if rest == absBase {
			return true
		}
		b, _, ok := parseVolumeName(rest)
		return ok && b == absBase
	}
}

// createArchive 는 inputs 를 담은 새 아카이브를 만든다. 진행 상황은 out, 경고는 errOut 에 쓴다.
func createArchive(opt addOptions, out, errOut io.Writer) (addResult, error) {
	var res addResult
	rep := &reporter{errOut: errOut}
	base := opt.archive

	if st, err := os.Stat(base); err == nil && st.IsDir() {
		return res, fmt.Errorf("%s is a directory", base)
	}
	// 같은 이름의 아카이브가 이미 있는지 일을 시작하기 전에 본다. 한 이름이 단일 파일과
	// 세트를 동시에 가리키면 읽을 때 모호하므로, 어느 쪽으로 만들든 둘 다 충돌이다.
	if !opt.overwrite {
		for _, p := range []string{base, volumeName(base, 1)} {
			if pathExists(p) {
				return res, fmt.Errorf("%s already exists (use -y to overwrite)", p)
			}
		}
	}

	c := &collector{seen: make(map[string]struct{}), isOutput: outputMatcher(base), rep: rep}
	for _, in := range opt.inputs {
		if err := c.addInput(in); err != nil {
			return res, err
		}
	}
	res.warnings = rep.warnings
	if len(c.entries) == 0 {
		return res, errors.New("nothing to add")
	}

	name := func(int) string { return base }
	if opt.volumeSize > 0 {
		name = func(i int) string { return volumeName(base, i) }
	}
	sw := newSplitWriter(name, opt.volumeSize)
	defer sw.Abort() // 성공해서 Commit 한 뒤에는 아무 일도 하지 않는다

	// zip.Writer 의 내부 버퍼는 4KB 라 그대로 두면 볼륨 파일에 4KB 단위로 write 가 나간다.
	// 큰 버퍼를 한 번만 할당해 시스템 콜 횟수를 줄인다.
	bw := bufio.NewWriterSize(sw, copyBufSize)
	zw := zip.NewWriter(bw)

	// flate.Writer 하나를 끝까지 재사용한다. 파일마다 새로 만들면 수백 KB 짜리
	// 압축기 상태를 매번 할당하게 되어, 작은 파일이 많을 때 할당 비용이 압축 비용을 넘는다.
	var fw *flate.Writer
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		if fw == nil {
			var err error
			if fw, err = flate.NewWriter(w, opt.level); err != nil {
				return nil, err
			}
		} else {
			fw.Reset(w)
		}
		return fw, nil
	})

	buf := make([]byte, copyBufSize)
	for _, e := range c.entries {
		fh, err := zip.FileInfoHeader(e.info)
		if err != nil {
			return res, fmt.Errorf("%s: %w", e.path, err)
		}
		fh.Name = e.name

		if e.info.IsDir() {
			fh.Method = zip.Store
			if _, err := zw.CreateHeader(fh); err != nil {
				return res, fmt.Errorf("%s: %w", e.path, err)
			}
			res.dirs++
			fmt.Fprintf(out, "  adding: %s\n", e.name)
			continue
		}

		// 헤더를 쓰기 전에 열어 본다. 읽을 수 없는 파일이면 흔적 없이 건너뛸 수 있다.
		f, err := os.Open(e.path)
		if err != nil {
			rep.warnf("%v", err)
			continue
		}
		fh.Method = zip.Deflate
		if opt.level == 0 || e.info.Size() == 0 {
			fh.Method = zip.Store
		}
		var n int64
		w, err := zw.CreateHeader(fh)
		if err == nil {
			n, err = io.CopyBuffer(w, onlyReader{f}, buf)
		}
		f.Close()
		if err != nil {
			// 엔트리를 쓰다 만 아카이브는 되살릴 수 없으므로 전체를 중단한다.
			return res, fmt.Errorf("%s: %w", e.path, err)
		}
		res.files++
		res.inBytes += n
		fmt.Fprintf(out, "  adding: %s\n", e.name)
	}
	res.warnings = rep.warnings

	if err := zw.Close(); err != nil {
		return res, err
	}
	if err := bw.Flush(); err != nil {
		return res, err
	}

	// 새 세트 바로 다음 번호부터 이어지는 기존 파일은 그대로 두면 새 세트의 일부로 읽혀
	// 아카이브가 깨진다. 단일 파일로 만들 때는 같은 이름의 옛 세트 전체(.001 부터)가 해당한다.
	// 번호가 떨어져 있는 파일은 세트로 읽힐 일이 없으므로 건드리지 않는다.
	finals := sw.Finals()
	next := 1
	if opt.volumeSize > 0 {
		next = len(finals) + 1
	}
	stale := volumesFrom(base, next)
	if !opt.overwrite {
		// 여기까지 왔다면 base 와 .001 은 없었다. 그래도 번호 붙은 파일이 새 볼륨 자리에 있거나
		// 바로 뒤에 이어질 수 있다. 덮어쓰거나 지우는 일은 -y 가 있을 때만 한다.
		for _, p := range finals {
			if pathExists(p) {
				return res, fmt.Errorf("%s already exists (use -y to overwrite)", p)
			}
		}
		if len(stale) > 0 {
			return res, fmt.Errorf("%s already exists and would be read as part of the new archive (use -y to remove it)", stale[0])
		}
	}
	if opt.volumeSize > 0 && pathExists(base) {
		stale = append(stale, base) // 분할로 다시 만들면 같은 이름의 단일 파일은 옛 아카이브다
	}

	vols, err := sw.Commit()
	if err != nil {
		return res, err
	}
	res.volumes = vols
	res.outBytes = sw.Written()

	// Commit 뒤에 지운다. 순서가 반대면 Commit 이 실패했을 때 옛 아카이브만 사라진다.
	for _, p := range stale {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return res, fmt.Errorf("archive written, but a stale file could not be removed (delete it by hand): %w", err)
		}
	}
	return res, nil
}
