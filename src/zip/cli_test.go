package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want options
	}{
		{"add basic",
			[]string{"a", "backup.zip", "f1", "f2"},
			options{command: "a", archive: "backup.zip", args: []string{"f1", "f2"}, level: 6}},
		{"add appends .zip when there is no extension",
			[]string{"a", "backup", "f1"},
			options{command: "a", archive: "backup.zip", args: []string{"f1"}, level: 6}},
		{"add keeps an explicit extension",
			[]string{"a", "backup.bak", "f1"},
			options{command: "a", archive: "backup.bak", args: []string{"f1"}, level: 6}},
		{"dot in a parent directory is not an extension",
			[]string{"a", filepath.Join("v1.2", "backup"), "f1"},
			options{command: "a", archive: filepath.Join("v1.2", "backup.zip"), args: []string{"f1"}, level: 6}},
		{"add with every switch",
			[]string{"a", "-v100m", "-m9", "-y", "backup.zip", "dir"},
			options{command: "a", archive: "backup.zip", args: []string{"dir"}, volumeSize: 100 << 20, level: 9, overwrite: true}},
		{"switches may follow the archive",
			[]string{"a", "backup.zip", "-m0", "f1", "-y"},
			options{command: "a", archive: "backup.zip", args: []string{"f1"}, level: 0, overwrite: true}},
		{"last duplicate switch wins",
			[]string{"a", "-m1", "-m9", "b.zip", "f"},
			options{command: "a", archive: "b.zip", args: []string{"f"}, level: 9}},
		{"uppercase command",
			[]string{"X", "b.zip"},
			options{command: "x", archive: "b.zip", level: 6}},
		{"extract with output dir and patterns",
			[]string{"x", "-oout dir", "-y", "b.zip.001", "*.txt", "docs"},
			options{command: "x", archive: "b.zip.001", args: []string{"*.txt", "docs"}, outDir: "out dir", overwrite: true, level: 6}},
		{"flat extract",
			[]string{"e", "-oout", "b.zip"},
			options{command: "e", archive: "b.zip", outDir: "out", level: 6}},
		{"list with pattern",
			[]string{"l", "b.zip", "*.go"},
			options{command: "l", archive: "b.zip", args: []string{"*.go"}, level: 6}},
		// 읽기 명령은 이름을 그대로 두고, 여는 쪽에서 .zip/.001 을 찾아본다.
		{"read commands keep the archive name as typed",
			[]string{"t", "b"},
			options{command: "t", archive: "b", level: 6}},
		{"double dash ends switches",
			[]string{"a", "b.zip", "--", "-weird", "--"},
			options{command: "a", archive: "b.zip", args: []string{"-weird", "--"}, level: 6}},
		{"single dash is a plain name",
			[]string{"a", "b.zip", "-"},
			options{command: "a", archive: "b.zip", args: []string{"-"}, level: 6}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseArgs(tc.args)
			if err != nil {
				t.Fatalf("parseArgs(%q): unexpected error: %v", tc.args, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseArgs(%q)\n got %+v\nwant %+v", tc.args, got, tc.want)
			}
		})
	}
}

func TestParseArgsRejects(t *testing.T) {
	bad := [][]string{
		{},
		{"z", "b.zip"},                  // 없는 명령
		{"ax", "b.zip"},                 // 명령은 한 글자
		{"a"},                           // 아카이브 없음
		{"a", "-m9"},                    // 스위치만 있고 아카이브 없음
		{"a", "b.zip"},                  // 추가할 대상 없음
		{"x"},                           // 아카이브 없음
		{"a", "-v", "b.zip", "f"},       // 값 없는 -v
		{"a", "-vabc", "b.zip", "f"},    // 숫자가 아닌 크기
		{"a", "-v1k", "b.zip", "f"},     // 최소 볼륨 크기 미만
		{"a", "-m", "b.zip", "f"},       // 값 없는 -m
		{"a", "-m10", "b.zip", "f"},     // 범위 밖
		{"a", "-mx", "b.zip", "f"},      // 숫자가 아님
		{"a", "-m-1", "b.zip", "f"},     // 음수
		{"a", "-q", "b.zip", "f"},       // 모르는 스위치
		{"a", "-yes", "b.zip", "f"},     // -y 뒤에 값
		{"a", "-oout", "b.zip", "f"},    // -o 는 x/e 전용
		{"x", "-v1m", "b.zip"},          // -v 는 a 전용
		{"x", "-m5", "b.zip"},           // -m 은 a 전용
		{"x", "-o", "b.zip"},            // 값 없는 -o
		{"l", "-oout", "b.zip"},         // 목록에는 출력 폴더가 없다
		{"l", "-y", "b.zip"},            // 덮어쓸 것이 없는 명령
		{"t", "-y", "b.zip"},            //
		{"a", "-v1m", "b.zip.001", "f"}, // 생성 이름에 볼륨 접미사
		{"a", "b.zip.001", "f"},         //
		{"x", "b.zip", "["},             // 잘못된 글롭 패턴
	}
	for _, args := range bad {
		if got, err := parseArgs(args); err == nil {
			t.Errorf("parseArgs(%q) = %+v, want error", args, got)
		}
	}
}

func TestRunUsage(t *testing.T) {
	t.Run("no arguments is an error", func(t *testing.T) {
		code, _, stderr := runCLI(t)
		if code != exitError || !strings.Contains(stderr, "Usage") {
			t.Errorf("exit %d, stderr %q; want exit %d with usage", code, stderr, exitError)
		}
	})
	for _, flag := range []string{"-h", "--help", "-?"} {
		t.Run("help "+flag, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, flag)
			if code != exitOK || !strings.Contains(stdout, "Usage") || stderr != "" {
				t.Errorf("exit %d, stdout %q, stderr %q; want exit 0 with usage on stdout", code, stdout, stderr)
			}
			// 사용자가 실제로 칠 스위치가 도움말에 빠지면 안 된다.
			for _, want := range []string{"-v<size>", "-m<0-9>", "-o<dir>", "-y", ".001"} {
				if !strings.Contains(stdout, want) {
					t.Errorf("usage is missing %q", want)
				}
			}
		})
	}
	t.Run("unknown command", func(t *testing.T) {
		code, stdout, stderr := runCLI(t, "z", "b.zip")
		if code != exitError || stderr == "" || stdout != "" {
			t.Errorf("exit %d, stdout %q, stderr %q; want exit %d with a message on stderr", code, stdout, stderr, exitError)
		}
	})
	t.Run("archive not found", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope.zip")
		for _, cmd := range []string{"x", "e", "l", "t"} {
			code, _, stderr := runCLI(t, cmd, missing)
			if code != exitError || !strings.Contains(stderr, "nope.zip") {
				t.Errorf("%s: exit %d, stderr %q; want exit %d naming the archive", cmd, code, stderr, exitError)
			}
		}
	})
}
