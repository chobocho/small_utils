package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 한글 자모 조합 상수 (유니코드 표준)
const (
	sBase  = 0xAC00 // '가'
	lBase  = 0x1100 // 초성 시작
	vBase  = 0x1161 // 중성 시작
	tBase  = 0x11A7 // 종성 시작 (종성 없음 = tBase)
	lCount = 19
	vCount = 21
	tCount = 28
	nCount = vCount * tCount
	sCount = lCount * nCount
)

func main() {
	// 옵션 설정
	dryRun := flag.Bool("n", false, "실제로 바꾸지 않고 변경될 이름만 출력합니다.")
	recursive := flag.Bool("r", false, "디렉터리 하위 파일까지 모두 처리합니다.")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("사용법: correctfile [-n] [-r] <파일명|디렉터리|와일드카드> ...")
		fmt.Println("  맥(NFD)에서 받은 깨진 한글 파일명을 정상(NFC) 한글로 고칩니다.")
		os.Exit(1)
	}

	for _, arg := range args {
		// Windows 셸은 와일드카드를 확장하지 않으므로 직접 처리
		matches, err := filepath.Glob(arg)
		if err != nil || len(matches) == 0 {
			matches = []string{arg}
		}
		for _, path := range matches {
			processPath(path, *recursive, *dryRun)
		}
	}
}

func processPath(path string, recursive, dryRun bool) {
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}

	if info.IsDir() && recursive {
		entries, err := os.ReadDir(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		// 하위 항목을 먼저 처리한 뒤 디렉터리 자신의 이름을 바꿈
		for _, e := range entries {
			processPath(filepath.Join(path, e.Name()), recursive, dryRun)
		}
	}

	renameFile(path, dryRun)
}

func renameFile(path string, dryRun bool) {
	dir, name := filepath.Split(path)
	fixed := composeHangul(name)
	if fixed == name {
		return
	}

	newPath := filepath.Join(dir, fixed)
	if _, err := os.Stat(newPath); err == nil {
		fmt.Fprintf(os.Stderr, "Skip: %s 이(가) 이미 존재합니다.\n", newPath)
		return
	}

	if dryRun {
		fmt.Printf("[dry-run] %s -> %s\n", name, fixed)
		return
	}

	if err := os.Rename(path, newPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error renaming %s: %v\n", path, err)
		return
	}
	fmt.Printf("%s -> %s\n", name, fixed)
}

// composeHangul 은 분리된 한글 자모(NFD)를 완성형 음절(NFC)로 조합합니다.
func composeHangul(s string) string {
	runes := []rune(s)
	var sb strings.Builder
	sb.Grow(len(s))

	for i := 0; i < len(runes); i++ {
		r := runes[i]

		// 초성 + 중성 [+ 종성]
		if isL(r) && i+1 < len(runes) && isV(runes[i+1]) {
			l := r - lBase
			v := runes[i+1] - vBase
			syllable := sBase + (l*vCount+v)*tCount
			i++
			if i+1 < len(runes) && isT(runes[i+1]) {
				syllable += runes[i+1] - tBase
				i++
			}
			sb.WriteRune(syllable)
			continue
		}

		// 종성 없는 완성형 음절 + 종성
		if isLV(r) && i+1 < len(runes) && isT(runes[i+1]) {
			sb.WriteRune(r + runes[i+1] - tBase)
			i++
			continue
		}

		sb.WriteRune(r)
	}
	return sb.String()
}

func isL(r rune) bool { return r >= lBase && r < lBase+lCount }
func isV(r rune) bool { return r >= vBase && r < vBase+vCount }
func isT(r rune) bool { return r > tBase && r < tBase+tCount }
func isLV(r rune) bool {
	return r >= sBase && r < sBase+sCount && (r-sBase)%tCount == 0
}
