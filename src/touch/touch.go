package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	// 옵션 설정: -c (파일이 없으면 생성하지 않음)
	noCreate := flag.Bool("c", false, "파일이 존재하지 않을 경우 생성하지 않습니다.")
	flag.Parse()

	// 인자로 받은 파일 목록 가져오기
	files := flag.Args()

	if len(files) == 0 {
		fmt.Println("사용법: touch [-c] <파일명1> [파일명2] ...")
		os.Exit(1)
	}

	now := time.Now()

	for _, filename := range files {
		err := touchFile(filename, now, *noCreate)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error touching %s: %v\n", filename, err)
		}
	}
}

func touchFile(filename string, now time.Time, noCreate bool) error {
	// 1. 파일 상태 확인
	_, err := os.Stat(filename)

	if os.IsNotExist(err) {
		// Case A: 파일이 없는 경우
		if noCreate {
			return nil // -c 옵션이 켜져 있으면 무시
		}

		// 빈 파일 생성
		file, err := os.Create(filename)
		if err != nil {
			return err
		}
		defer file.Close()
		// 생성 시점의 시간은 자동으로 현재 시간이 됨
		return nil
	} else if err != nil {
		// 권한 문제 등 기타 에러
		return err
	}

	// Case B: 파일이 이미 있는 경우 -> 시간 갱신 (AccessTime, ModifyTime 모두 현재로)
	return os.Chtimes(filename, now, now)
}
