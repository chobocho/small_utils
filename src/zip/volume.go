package main

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	// 최소 볼륨 크기. Info-ZIP `zip -s` 와 같은 하한이며, -v100 같은 오타 하나로
	// 수십만 개의 조각 파일이 생기는 사고를 막는다.
	minVolumeSize = 64 << 10

	// 기록 중인 파일에 붙는 접미사. 끝까지 성공해야 최종 이름으로 바꾸므로
	// 실패하거나 중단돼도 기존 아카이브는 그대로 남는다.
	partSuffix = ".part"

	// 분할 세트를 읽을 때 동시에 열어 두는 볼륨 수. 볼륨이 수천 개여도
	// 프로세스의 파일 핸들 한도(보통 1024)에 걸리지 않게 한다.
	maxOpenVolumes = 8
)

// parseVolumeSize 는 -v 뒤의 크기를 바이트 수로 바꾼다.
// 숫자만 쓰면 바이트이고 K/M/G/T(1024 배수, 대소문자 무관, 뒤에 B·iB 허용)를 붙일 수 있다.
func parseVolumeSize(s string) (int64, error) {
	// strconv 는 부호·지수·Inf·16진 표기까지 받아 주므로 숫자부를 직접 잘라 낸다.
	i, digits, dots := 0, 0, 0
scan:
	for ; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digits++
		case c == '.':
			dots++
		default:
			break scan
		}
	}
	if digits == 0 || dots > 1 {
		return 0, fmt.Errorf("invalid volume size %q", s)
	}

	var mult int64
	switch strings.ToLower(s[i:]) {
	case "", "b":
		mult = 1
	case "k", "kb", "kib":
		mult = 1 << 10
	case "m", "mb", "mib":
		mult = 1 << 20
	case "g", "gb", "gib":
		mult = 1 << 30
	case "t", "tb", "tib":
		mult = 1 << 40
	default:
		return 0, fmt.Errorf("invalid volume size %q (unknown unit %q)", s, s[i:])
	}

	var n int64
	if dots == 0 {
		v, err := strconv.ParseInt(s[:i], 10, 64)
		if err != nil || v > math.MaxInt64/mult {
			return 0, fmt.Errorf("volume size %q is too large", s)
		}
		n = v * mult
	} else {
		f, err := strconv.ParseFloat(s[:i], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid volume size %q", s)
		}
		f = math.Ceil(f * float64(mult))
		// float64(MaxInt64) 는 2^63 으로 올림되므로 >= 로 비교해야 넘침을 놓치지 않는다.
		if f >= float64(math.MaxInt64) {
			return 0, fmt.Errorf("volume size %q is too large", s)
		}
		n = int64(f)
	}
	if n < minVolumeSize {
		return 0, fmt.Errorf("volume size %q is below the minimum of 64K", s)
	}
	return n, nil
}

// volumeName 은 index(1부터)번째 볼륨의 경로다. 7-Zip 과 같은 규칙이라
// 서로의 분할 세트를 그대로 읽을 수 있고, 999 를 넘으면 자릿수가 늘어난다.
func volumeName(base string, index int) string {
	return fmt.Sprintf("%s.%03d", base, index)
}

// splitVolumeSuffix 는 "a.zip.001" 을 ("a.zip", true) 로 나눈다.
// 볼륨 이름이 아니면 받은 이름을 그대로 돌려준다.
func splitVolumeSuffix(name string) (string, bool) {
	dot := strings.LastIndexByte(name, '.')
	// 접미사 앞에 파일 이름이 있어야 한다("dir/.001" 이나 ".001" 은 볼륨이 아니다).
	if dot <= 0 || os.IsPathSeparator(name[dot-1]) {
		return name, false
	}
	suffix := name[dot+1:]
	if len(suffix) < 3 {
		return name, false
	}
	for i := 0; i < len(suffix); i++ {
		if suffix[i] < '0' || suffix[i] > '9' {
			return name, false
		}
	}
	return name[:dot], true
}

// parseVolumeName 은 "a.zip.012" 를 ("a.zip", 12, true) 로 나눈다.
// czip 이 붙이는 정규 표기만 인정하므로 "a.zip.0012" 같은 이름은 볼륨이 아니다.
func parseVolumeName(name string) (string, int, bool) {
	base, ok := splitVolumeSuffix(name)
	if !ok {
		return name, 0, false
	}
	index, err := strconv.Atoi(name[len(base)+1:])
	if err != nil || index < 1 || volumeName(base, index) != name {
		return name, 0, false
	}
	return base, index, true
}

// volumesFrom 은 base 의 from 번 볼륨부터 번호가 끊기지 않고 이어지는 파일을 돌려준다.
// 끊긴 뒤의 파일은 세트로 보지 않는다: backup.zip.20261119 처럼 날짜를 붙인 사본도
// 볼륨과 같은 모양이라, 폴더에서 번호 모양 이름을 전부 긁어모으면 남의 파일을
// 세트로 착각해 지우거나 "볼륨 누락" 으로 오판하게 된다.
// O(V) 번의 stat: 폴더 크기와 무관하게 볼륨 수에만 비례한다.
func volumesFrom(base string, from int) []string {
	var vols []string
	for i := from; ; i++ {
		p := volumeName(base, i)
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			return vols
		}
		vols = append(vols, p)
	}
}

// findVolumes 는 base.001 부터 이어지는 분할 세트를 번호순으로 돌려준다. 없으면 nil.
func findVolumes(base string) []string { return volumesFrom(base, 1) }

// missingVolume 은 세트가 have 개에서 끊겼는데 그보다 큰 번호의 파일이 폴더에 더 있으면,
// 비어 있는 첫 볼륨의 이름을 돌려준다(없으면 ""). 아카이브가 열리지 않을 때 원인을
// 짚어 주는 용도라 정상 경로에서는 부르지 않는다. O(D): D 는 폴더 항목 수.
func missingVolume(base string, have int) string {
	dir, name := filepath.Split(base)
	if dir == "" {
		dir = "."
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if b, index, ok := parseVolumeName(e.Name()); ok && b == name && index > have {
			return volumeName(base, have+1)
		}
	}
	return ""
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// splitWriter 는 받은 바이트를 limit 크기의 볼륨 파일들로 나눠 쓴다.
// zip.Writer 는 뒤로 돌아가 고쳐 쓰지 않는 순차 기록이라 io.Writer 만으로 충분하고,
// 그래서 볼륨들을 이어 붙이면 그대로 하나의 표준 ZIP 이 된다.
type splitWriter struct {
	name  func(index int) string // index(1부터)번째 볼륨의 최종 경로
	limit int64                  // 볼륨 크기. 0 이하면 나누지 않는다(단일 파일)

	cur       *os.File
	curSize   int64
	finals    []string // 지금까지 연 볼륨의 최종 경로
	committed int      // 그중 최종 이름으로 바뀐 개수
	written   int64
	done      bool
}

func newSplitWriter(name func(index int) string, limit int64) *splitWriter {
	return &splitWriter{name: name, limit: limit}
}

func (w *splitWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		// 쓸 데이터가 실제로 있을 때만 다음 볼륨을 연다. 그래야 정확히 N 바이트를
		// 썼을 때 빈 볼륨이 하나 더 생기지 않는다.
		if w.cur == nil || (w.limit > 0 && w.curSize >= w.limit) {
			if err := w.next(); err != nil {
				return total, err
			}
		}
		chunk := p
		if w.limit > 0 {
			if room := w.limit - w.curSize; int64(len(chunk)) > room {
				chunk = chunk[:room]
			}
		}
		n, err := w.cur.Write(chunk)
		w.curSize += int64(n)
		w.written += int64(n)
		total += n
		if err != nil {
			return total, err
		}
		p = p[n:]
	}
	return total, nil
}

func (w *splitWriter) next() error {
	if err := w.closeCurrent(); err != nil {
		return err
	}
	final := w.name(len(w.finals) + 1)
	f, err := os.OpenFile(final+partSuffix, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	w.finals = append(w.finals, final)
	w.cur, w.curSize = f, 0
	return nil
}

func (w *splitWriter) closeCurrent() error {
	if w.cur == nil {
		return nil
	}
	err := w.cur.Close()
	w.cur = nil
	return err
}

// Written 은 지금까지 볼륨에 쓴 총 바이트 수다.
func (w *splitWriter) Written() int64 { return w.written }

// Finals 는 Commit 이 만들어 낼 최종 경로들이다. 이름을 바꾸기 전에 충돌을 확인하는 데 쓴다.
func (w *splitWriter) Finals() []string { return w.finals }

// Commit 은 임시 파일들을 최종 이름으로 바꾸고 그 경로를 돌려준다.
func (w *splitWriter) Commit() ([]string, error) {
	if err := w.closeCurrent(); err != nil {
		return nil, err
	}
	for w.committed < len(w.finals) {
		final := w.finals[w.committed]
		if err := os.Rename(final+partSuffix, final); err != nil {
			return nil, err
		}
		w.committed++
	}
	w.done = true
	return w.finals, nil
}

// Abort 는 만들던 파일을 모두 지운다. Commit 이 끝난 뒤에는 아무 일도 하지 않으므로
// defer 로 걸어 두어도 안전하다.
func (w *splitWriter) Abort() {
	if w.done {
		return
	}
	_ = w.closeCurrent()
	for i, final := range w.finals {
		if i < w.committed {
			// Commit 도중 실패한 경우: 일부만 바뀐 세트는 읽을 수 없으니 함께 지운다.
			_ = os.Remove(final)
		} else {
			_ = os.Remove(final + partSuffix)
		}
	}
	w.finals, w.committed = nil, 0
}

// multiReaderAt 는 여러 볼륨 파일을 이어 붙인 하나의 io.ReaderAt 로 보이게 한다.
// zip.Reader 가 끝의 중앙 디렉터리부터 임의 접근으로 읽기 때문에 순차 Reader 로는 부족하다.
type multiReaderAt struct {
	mu    sync.Mutex
	paths []string
	offs  []int64 // offs[i] 는 볼륨 i 의 시작 오프셋, offs[len(paths)] 는 전체 크기

	open map[int]*os.File
	lru  []int // 열려 있는 볼륨 번호, 가장 최근에 쓴 것이 맨 앞
}

func openMultiReaderAt(paths []string) (*multiReaderAt, error) {
	if len(paths) == 0 {
		return nil, errors.New("no archive files to open")
	}
	m := &multiReaderAt{
		paths: paths,
		offs:  make([]int64, len(paths)+1),
		open:  make(map[int]*os.File, maxOpenVolumes),
	}
	for i, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if st.IsDir() {
			return nil, fmt.Errorf("%s is a directory", p)
		}
		m.offs[i+1] = m.offs[i] + st.Size()
	}
	return m, nil
}

// Size 는 모든 볼륨을 이어 붙인 전체 길이다.
func (m *multiReaderAt) Size() int64 { return m.offs[len(m.paths)] }

// ReadAt 은 io.ReaderAt 계약을 따른다: 버퍼를 다 채우지 못하면 반드시 오류(끝이면 io.EOF)를 준다.
// 조각 하나당 O(log V) 시간(V 는 볼륨 수), 추가 공간 O(1).
func (m *multiReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("czip: negative read offset")
	}
	// 핸들 캐시가 공유 상태라 호출 전체를 잠근다. czip 은 한 번에 한 엔트리만 읽으므로 경합은 없다.
	m.mu.Lock()
	defer m.mu.Unlock()

	size := m.Size()
	n := 0
	for n < len(p) {
		if off >= size {
			return n, io.EOF
		}
		// offs 가 오름차순이므로 이분 탐색으로 off 를 담은 볼륨을 찾는다.
		// "끝 오프셋이 off 보다 큰 첫 볼륨" 조건이라 크기 0 인 볼륨은 저절로 건너뛴다.
		i := sort.Search(len(m.paths), func(i int) bool { return m.offs[i+1] > off })
		f, err := m.file(i)
		if err != nil {
			return n, err
		}
		chunk := p[n:]
		if room := m.offs[i+1] - off; int64(len(chunk)) > room {
			chunk = chunk[:room]
		}
		k, err := f.ReadAt(chunk, off-m.offs[i])
		n += k
		off += int64(k)
		if err != nil {
			// 처음 잰 크기 안쪽만 읽으므로 여기서 EOF 가 나면 볼륨이 그사이 줄어든 것이다.
			if err == io.EOF {
				err = fmt.Errorf("%s: %w", m.paths[i], io.ErrUnexpectedEOF)
			}
			return n, err
		}
	}
	return n, nil
}

// file 은 볼륨 i 의 핸들을 돌려준다. maxOpenVolumes 를 넘으면 가장 오래 안 쓴 것을 닫는다.
// lru 길이가 상수(8)라 선형 이동으로 충분하다.
func (m *multiReaderAt) file(i int) (*os.File, error) {
	if len(m.lru) > 0 && m.lru[0] == i { // 순차 읽기에서 거의 항상 여기로 온다
		return m.open[i], nil
	}
	if f, ok := m.open[i]; ok {
		for k, v := range m.lru {
			if v == i {
				copy(m.lru[1:k+1], m.lru[:k])
				m.lru[0] = i
				break
			}
		}
		return f, nil
	}
	if len(m.lru) >= maxOpenVolumes {
		last := m.lru[len(m.lru)-1]
		_ = m.open[last].Close()
		delete(m.open, last)
		m.lru = m.lru[:len(m.lru)-1]
	}
	f, err := os.Open(m.paths[i])
	if err != nil {
		return nil, err
	}
	m.open[i] = f
	m.lru = append(m.lru, 0)
	copy(m.lru[1:], m.lru)
	m.lru[0] = i
	return f, nil
}

func (m *multiReaderAt) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var first error
	for i, f := range m.open {
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
		delete(m.open, i)
	}
	m.lru = m.lru[:0]
	return first
}
