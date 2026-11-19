package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// archive 는 열린 아카이브다. 단일 파일이든 분할 세트든 같은 방식으로 다룬다.
type archive struct {
	*zip.Reader
	src     *multiReaderAt
	volumes []string
}

// resolveArchive 는 사용자가 친 이름을 실제 파일 목록으로 바꾼다.
// 분할 세트는 기본 이름, 아무 볼륨 이름, 확장자 생략 어느 쪽으로 줘도 찾는다.
func resolveArchive(name string) ([]string, error) {
	isFile := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir()
	}

	// 볼륨 이름(.NNN)을 직접 준 경우: 그 파일이 .001 부터 이어지는 세트에 속하면 세트 전체를 연다.
	if base, index, ok := parseVolumeName(name); ok && isFile(name) {
		vols := findVolumes(base)
		if index <= len(vols) {
			return vols, nil
		}
		// 세트에 속하지 않는다. 숫자 확장자를 가진 평범한 ZIP(data.2024)이거나,
		// 앞쪽 볼륨이 빠진 세트의 조각이다. 혼자서 온전한 ZIP 인지로 가른다.
		if isWholeZip(name) {
			return []string{name}, nil
		}
		return nil, fmt.Errorf("volume %s is missing", volumeName(base, len(vols)+1))
	}

	candidates := []string{name}
	if filepath.Ext(filepath.Base(name)) == "" {
		candidates = append(candidates, name+".zip")
	}
	// 단일 파일을 먼저 찾는다. 세트부터 찾으면 data.zip 이 있는데도 옆의 data.001 이 먼저 잡힌다.
	for _, c := range candidates {
		if isFile(c) {
			return []string{c}, nil
		}
	}
	for _, c := range candidates {
		if vols := findVolumes(c); len(vols) > 0 {
			return vols, nil
		}
	}
	for _, c := range candidates {
		if missing := missingVolume(c, 0); missing != "" {
			return nil, fmt.Errorf("%s: archive not found (volume %s is missing)", name, missing)
		}
	}
	if st, err := os.Stat(name); err == nil && st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", name)
	}
	return nil, fmt.Errorf("%s: archive not found", name)
}

// isWholeZip 은 파일 하나가 그 자체로 완결된 ZIP 인지 본다. 분할 세트의 마지막 볼륨은
// 중앙 디렉터리를 갖고 있어 열리기는 하지만, 첫 엔트리의 데이터가 앞 볼륨에 있으므로
// 첫 엔트리의 로컬 헤더를 찾아보는 것으로 구분된다.
func isWholeZip(p string) bool {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return false
	}
	defer zr.Close()
	if len(zr.File) == 0 {
		return true
	}
	_, err = zr.File[0].DataOffset()
	return err == nil
}

func openArchive(name string) (*archive, error) {
	vols, err := resolveArchive(name)
	if err != nil {
		return nil, err
	}
	src, err := openMultiReaderAt(vols)
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(src, src.Size())
	// ErrInsecurePath 는 경고성 오류라 Reader 가 함께 돌아온다. 경로는 safeEntryPath 가
	// 엔트리마다 따로 검사하므로, 위험한 엔트리 하나 때문에 전체를 못 여는 일은 없게 한다.
	if err != nil && !(errors.Is(err, zip.ErrInsecurePath) && zr != nil) {
		src.Close()
		base, _, isVolume := parseVolumeName(vols[0])
		if !isVolume {
			return nil, fmt.Errorf("%s: %v", vols[0], err)
		}
		// 세트가 열리지 않을 때에만 폴더를 훑어 원인을 찾는다. 이어지는 볼륨들이 온전한 ZIP 이면
		// 번호가 동떨어진 다른 파일이 있어도 문제가 아니기 때문이다.
		if missing := missingVolume(base, len(vols)); missing != "" {
			return nil, fmt.Errorf("volume %s is missing", missing)
		}
		// 중앙 디렉터리는 마지막 볼륨에 있어서, 끝 볼륨이 빠지면 번호만으로는 알 수 없다.
		return nil, fmt.Errorf("%s: %v (the last volume may be missing, or a volume is damaged)", vols[0], err)
	}
	return &archive{Reader: zr, src: src, volumes: vols}, nil
}

func (a *archive) Close() error { return a.src.Close() }

// describe 는 출력 머리줄에 쓸 아카이브 이름이다.
func (a *archive) describe() string {
	if n := len(a.volumes); n > 1 {
		return fmt.Sprintf("%s .. %s (%d volumes)", a.volumes[0], filepath.Base(a.volumes[n-1]), n)
	}
	return a.volumes[0]
}

// slashName 은 윈도우 도구가 남긴 '\' 구분자를 '/' 로 통일한다.
func slashName(name string) string { return strings.ReplaceAll(name, `\`, "/") }

var errUnsafePath = errors.New("unsafe path")

// safeEntryPath 는 엔트리 이름이 대상 폴더 안쪽만 가리키는지 확인하고 정리된 상대 경로('/' 구분)를 돌려준다.
// 절대 경로와 '..' 성분은 결과가 안쪽이더라도 거부한다(zip-slip 방지). 고쳐 쓰지 않고 거부하는 이유는
// 그런 이름이 정상 도구에서는 나오지 않아, 조용히 다른 위치에 푸는 것보다 알리는 편이 안전해서다.
func safeEntryPath(name string) (string, error) {
	if strings.IndexByte(name, 0) >= 0 {
		return "", errUnsafePath
	}
	// '\' 도 구분자로 봐야 `..\x` 같은 탈출 시도가 운영체제와 무관하게 걸린다.
	n := slashName(name)
	if strings.HasPrefix(n, "/") {
		return "", errUnsafePath
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return "", errUnsafePath
		}
	}
	clean := path.Clean(n)
	// IsLocal 은 윈도우의 드라이브 문자와 예약 이름(NUL, COM1 …)까지 걸러 준다.
	if clean == "." || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", errUnsafePath
	}
	return clean, nil
}

// matchEntry 는 엔트리가 패턴 중 하나에 걸리는지 본다. 패턴이 없으면 전부 걸린다.
//   - '/' 가 없는 패턴은 깊이와 무관하게 이름 한 토막에 적용한다(*.txt, node_modules)
//   - '/' 가 있는 패턴은 전체 경로에 적용한다(docs/*.md). '*' 는 '/' 를 넘지 않는다
//   - 폴더가 걸리면 그 아래 전부가 걸린다
//
// O(P·D): P 는 패턴 수, D 는 경로 깊이.
func matchEntry(patterns []string, name string) bool {
	if len(patterns) == 0 {
		return true
	}
	name = strings.TrimSuffix(name, "/")
	for _, p := range patterns {
		p = strings.TrimSuffix(filepath.ToSlash(p), "/")
		byName := !strings.Contains(p, "/")
		// 엔트리 자신에서 시작해 상위 폴더로 올라가며 검사한다.
		for n := name; n != "" && n != "." && n != "/"; n = path.Dir(n) {
			if ok, _ := path.Match(p, n); ok {
				return true
			}
			if byName {
				if ok, _ := path.Match(p, path.Base(n)); ok {
					return true
				}
			}
		}
	}
	return false
}

func isDirEntry(f *zip.File) bool {
	return f.Mode().IsDir() || strings.HasSuffix(slashName(f.Name), "/")
}

// ZIP 일반 목적 플래그의 비트 0: 엔트리가 암호화돼 있음.
const flagEncrypted = 0x1

var (
	errEncrypted = errors.New("encrypted entries are not supported")
	errExists    = errors.New("already exists")
)

type extractOptions struct {
	outDir    string
	overwrite bool
	flat      bool // e 명령: 경로를 버리고 한 폴더에 푼다
	patterns  []string
}

type opResult struct {
	matched  int
	files    int
	dirs     int
	bytes    int64
	packed   int64
	warnings int
	errors   int
}

func extractArchive(a *archive, opt extractOptions, out, errOut io.Writer) opResult {
	var res opResult
	rep := &reporter{errOut: errOut}
	buf := make([]byte, copyBufSize)

	for _, f := range a.File {
		name := slashName(f.Name)
		if !matchEntry(opt.patterns, name) {
			continue
		}
		res.matched++

		rel, err := safeEntryPath(f.Name)
		if err != nil {
			rep.warnf("%s: unsafe path, skipped", name)
			continue
		}
		dir := isDirEntry(f)
		if opt.flat {
			if dir {
				continue
			}
			rel = path.Base(rel)
		}
		target := filepath.Join(opt.outDir, filepath.FromSlash(rel))

		switch {
		case dir:
			if err := os.MkdirAll(target, 0o777); err != nil {
				rep.errorf("%v", err)
				continue
			}
			res.dirs++
			fmt.Fprintf(out, "   creating: %s/\n", rel)
		case f.Mode()&fs.ModeSymlink != 0:
			// 링크를 복원하면 뒤따르는 엔트리가 그 링크를 타고 대상 폴더 밖에 쓰일 수 있다.
			rep.warnf("%s: symbolic link, skipped", name)
		case !f.Mode().IsRegular():
			rep.warnf("%s: not a regular file, skipped", name)
		case f.Flags&flagEncrypted != 0:
			rep.errorf("%s: %v", name, errEncrypted)
		default:
			n, err := extractFile(f, target, opt.overwrite, buf)
			switch {
			case errors.Is(err, errExists):
				rep.warnf("%s: already exists, skipped (use -y to overwrite)", target)
			case err != nil:
				rep.errorf("%s: %v", name, err)
			default:
				res.files++
				res.bytes += n
				fmt.Fprintf(out, " extracting: %s\n", rel)
			}
		}
	}
	res.warnings, res.errors = rep.warnings, rep.errors
	return res
}

// extractFile 은 같은 폴더의 임시 파일에 먼저 풀고 CRC 까지 맞은 뒤에야 제자리로 옮긴다.
// 그래서 손상된 내용이 정상 파일처럼 남지 않고, -y 로 덮어쓰다 실패해도 기존 파일이 보존된다.
func extractFile(f *zip.File, target string, overwrite bool, buf []byte) (int64, error) {
	if _, err := os.Lstat(target); err == nil {
		if !overwrite {
			return 0, errExists
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return 0, err
	}
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	// 실행 비트 등 권한만 되살린다. setuid 같은 특수 비트는 Perm() 에서 빠진다.
	perm := f.Mode().Perm()
	if perm == 0 {
		perm = 0o666
	}
	tmp, err := createTemp(dir, perm)
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	// CRC 불일치는 스트림 끝의 Read 에서 zip.ErrChecksum 으로 돌아온다.
	n, err := io.CopyBuffer(onlyWriter{tmp}, rc, buf)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if !f.Modified.IsZero() {
			// 시간 복원은 부가 기능이다. 지원하지 않는 파일시스템에서 해제 자체를 실패시키지 않는다.
			_ = os.Chtimes(tmpName, f.Modified, f.Modified)
		}
		err = os.Rename(tmpName, target)
	}
	if err != nil {
		_ = os.Remove(tmpName)
		return 0, err
	}
	return n, nil
}

var tempSeq int

// createTemp 는 대상과 같은 폴더에 임시 파일을 만든다(같은 파일시스템이어야 rename 이 원자적이다).
// os.CreateTemp 는 권한이 0600 고정이라 쓰지 않는다. 여기서 perm 으로 직접 만들어야
// 아카이브의 권한이 사용자의 umask 를 거쳐 적용된다.
func createTemp(dir string, perm fs.FileMode) (*os.File, error) {
	for try := 0; ; try++ {
		tempSeq++
		name := filepath.Join(dir, fmt.Sprintf(".czip-%d-%d.tmp", os.Getpid(), tempSeq))
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		// 이름이 겹칠 때만(이전 실행이 남긴 파일) 다음 번호로 다시 시도한다.
		if err == nil || !errors.Is(err, fs.ErrExist) || try >= 100 {
			return f, err
		}
	}
}

func listArchive(a *archive, patterns []string, out io.Writer) opResult {
	var res opResult
	const row = "%12s  %12s  %5s  %-16s  %s\n"
	rule := fmt.Sprintf(row, "------------", "------------", "-----", "----------------", "----")
	fmt.Fprintf(out, row, "Length", "Packed", "Ratio", "Modified", "Name")
	fmt.Fprint(out, rule)
	for _, f := range a.File {
		name := slashName(f.Name)
		if !matchEntry(patterns, name) {
			continue
		}
		res.matched++
		if isDirEntry(f) {
			res.dirs++
		} else {
			res.files++
		}
		res.bytes += int64(f.UncompressedSize64)
		res.packed += int64(f.CompressedSize64)
		fmt.Fprintf(out, row,
			fmt.Sprint(f.UncompressedSize64), fmt.Sprint(f.CompressedSize64),
			savedPercent(int64(f.CompressedSize64), int64(f.UncompressedSize64)),
			f.Modified.Local().Format("2006-01-02 15:04"), name)
	}
	fmt.Fprint(out, rule)
	fmt.Fprintf(out, row, fmt.Sprint(res.bytes), fmt.Sprint(res.packed),
		savedPercent(res.packed, res.bytes), "",
		fmt.Sprintf("%d file(s), %d dir(s)", res.files, res.dirs))
	return res
}

// savedPercent 는 압축으로 줄어든 비율이다. 무압축이나 빈 파일, 그리고 헤더 때문에
// 오히려 커진 경우(아주 작은 파일)는 음수 대신 0% 로 표시한다.
func savedPercent(packed, length int64) string {
	if length <= 0 || packed >= length {
		return "0%"
	}
	// 반올림하지 않고 버린다: 99.7% 를 "100%" 로 보여 주면 크기가 0 이 된 것처럼 읽힌다.
	return fmt.Sprintf("%d%%", int(100*(1-float64(packed)/float64(length))))
}

func testArchive(a *archive, patterns []string, out, errOut io.Writer) opResult {
	var res opResult
	rep := &reporter{errOut: errOut}
	buf := make([]byte, copyBufSize)
	for _, f := range a.File {
		name := slashName(f.Name)
		if !matchEntry(patterns, name) {
			continue
		}
		res.matched++
		if isDirEntry(f) {
			res.dirs++
			continue
		}
		err := errEncrypted
		if f.Flags&flagEncrypted == 0 {
			err = verifyEntry(f, buf)
		}
		if err != nil {
			rep.errorf("%s: %v", name, err)
			continue
		}
		res.files++
		res.bytes += int64(f.UncompressedSize64)
		fmt.Fprintf(out, "    testing: %s  OK\n", name)
	}
	res.warnings, res.errors = rep.warnings, rep.errors
	return res
}

// verifyEntry 는 엔트리를 끝까지 풀어 본다. zip.Reader 가 스트림 끝에서
// CRC-32 와 길이를 대조하므로 끝까지 읽는 것만으로 무결성이 확인된다.
func verifyEntry(f *zip.File, buf []byte) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.CopyBuffer(onlyWriter{io.Discard}, rc, buf)
	return err
}
