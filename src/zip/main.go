// czip 은 분할 볼륨을 지원하는 ZIP 압축/해제 도구다. 명령 체계는 ARJ 를 따른다.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const version = "V0.1.0"

// 종료 코드. 스크립트에서 "일부 건너뜀" 과 "실패" 를 구분할 수 있게 나눈다.
const (
	exitOK    = 0
	exitWarn  = 1 // 끝까지 갔지만 건너뛴 것이 있음
	exitError = 2
)

// 파일 복사와 아카이브 출력에 쓰는 버퍼 크기. 기본값(32KB)보다 크게 잡아
// 큰 파일에서 시스템 콜 횟수를 줄이되, 실행당 한두 번만 할당한다.
const copyBufSize = 256 << 10

const usageText = `czip ` + version + ` - ZIP archiver with split volumes (ARJ-style commands)

Usage: czip <command> [-switch...] <archive> [file|pattern...]

Commands:
  a   add files and directories to a new archive (directories are recursed)
  x   extract with full paths
  e   extract without paths (everything into one directory)
  l   list contents
  t   test integrity (CRC)

Switches:
  -v<size>  split into volumes of <size> (a): -v1440k, -v100m, -v1g; minimum 64k
  -m<0-9>   compression level (a): 0 = store, 1 = fastest, 9 = best; default 6
  -o<dir>   output directory (x, e); default is the current directory
  -y        overwrite existing files, or an existing archive (a, x, e)
  --        treat everything after it as names, not switches

Split volumes are named <archive>.001, <archive>.002, ... (same as 7-Zip).
For x/e/l/t pass <archive> or any volume. To rejoin by hand:
  cat <archive>.* > <archive>

Patterns select entries for x/e/l/t: '*.txt' matches a name at any depth,
'docs/*.md' matches a full path, and naming a directory selects all below it.

Exit codes: 0 = success, 1 = warning (something was skipped), 2 = error

Examples:
  czip a backup.zip docs photos
  czip a -v100m -m9 backup.zip photos
  czip l backup.zip
  czip x -orestore backup.zip.001
  czip e -oflat backup.zip '*.jpg'
  czip t backup.zip
`

type options struct {
	command    string // a, x, e, l, t 중 하나(소문자)
	archive    string
	args       []string // a 는 넣을 경로, 나머지는 엔트리 패턴
	volumeSize int64
	level      int
	outDir     string
	overwrite  bool
}

// 스위치별로 의미가 있는 명령. 뜻 없는 조합(-v 로 해제 등)은 조용히 무시하지 않고
// 오류로 알린다: 명령 글자를 잘못 친 경우를 초기에 잡기 위해서다.
var switchCommands = map[byte]string{
	'v': "a",
	'm': "a",
	'o': "xe",
	'y': "axe",
}

func parseArgs(args []string) (options, error) {
	opt := options{level: 6}
	if len(args) == 0 {
		return opt, errors.New("missing command")
	}
	cmd := strings.ToLower(args[0])
	if len(cmd) != 1 || !strings.Contains("axelt", cmd) {
		return opt, fmt.Errorf("unknown command %q", args[0])
	}
	opt.command = cmd

	var names []string
	switches := true
	for _, arg := range args[1:] {
		// "-" 한 글자는 스위치가 아니라 이름으로 본다.
		if !switches || len(arg) < 2 || arg[0] != '-' {
			names = append(names, arg)
			continue
		}
		if arg == "--" {
			switches = false
			continue
		}
		key, val := arg[1], arg[2:]
		cmds, known := switchCommands[key]
		if !known {
			return opt, fmt.Errorf("unknown switch %q", arg)
		}
		if !strings.Contains(cmds, cmd) {
			return opt, fmt.Errorf("switch -%c cannot be used with command %q", key, cmd)
		}
		switch key {
		case 'v':
			if val == "" {
				return opt, errors.New("switch -v needs a size, e.g. -v100m")
			}
			size, err := parseVolumeSize(val)
			if err != nil {
				return opt, err
			}
			opt.volumeSize = size
		case 'm':
			if len(val) != 1 || val[0] < '0' || val[0] > '9' {
				return opt, fmt.Errorf("switch -m needs a level from 0 to 9, got %q", arg)
			}
			opt.level = int(val[0] - '0')
		case 'o':
			if val == "" {
				return opt, errors.New("switch -o needs a directory, e.g. -oout")
			}
			opt.outDir = val
		case 'y':
			if val != "" {
				return opt, fmt.Errorf("unknown switch %q", arg)
			}
			opt.overwrite = true
		}
	}

	if len(names) == 0 {
		return opt, errors.New("missing archive name")
	}
	opt.archive = names[0]
	if len(names) > 1 {
		opt.args = names[1:]
	}

	if cmd == "a" {
		// 볼륨 접미사는 czip 이 붙인다. 받아 주면 backup.zip.001.001 이 만들어진다.
		if _, isVolume := splitVolumeSuffix(opt.archive); isVolume {
			return opt, fmt.Errorf("archive name %q must not end with a volume number; czip adds .001, .002, ... itself", opt.archive)
		}
		if filepath.Ext(filepath.Base(opt.archive)) == "" {
			opt.archive += ".zip"
		}
		if len(opt.args) == 0 {
			return opt, errors.New("nothing to add: name at least one file or directory")
		}
		return opt, nil
	}
	for _, p := range opt.args {
		// 잘못된 패턴은 아무것도 안 걸리는 것처럼 보이므로 미리 걸러 낸다.
		if _, err := path.Match(strings.TrimSuffix(filepath.ToSlash(p), "/"), ""); err != nil {
			return opt, fmt.Errorf("bad pattern %q", p)
		}
	}
	return opt, nil
}

// reporter 는 경고와 오류를 세면서 errOut 에 쓴다. 종료 코드는 이 횟수로 정해진다.
type reporter struct {
	errOut   io.Writer
	warnings int
	errors   int
}

func (r *reporter) warnf(format string, args ...any) {
	r.warnings++
	fmt.Fprintf(r.errOut, "czip: warning: %s\n", fmt.Sprintf(format, args...))
}

func (r *reporter) errorf(format string, args ...any) {
	r.errors++
	fmt.Fprintf(r.errOut, "czip: %s\n", fmt.Sprintf(format, args...))
}

// onlyReader/onlyWriter 는 *os.File 의 WriteTo/ReadFrom 을 가린다. 그대로 두면 io.CopyBuffer 가
// 우리가 준 버퍼를 무시하고 파일마다 32KB 버퍼를 새로 할당하는 경로로 빠진다.
type onlyReader struct{ io.Reader }
type onlyWriter struct{ io.Writer }

func exitCode(warnings, errs int) int {
	switch {
	case errs > 0:
		return exitError
	case warnings > 0:
		return exitWarn
	}
	return exitOK
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return exitError
	}
	switch args[0] {
	case "-h", "--help", "-?":
		fmt.Fprint(stdout, usageText)
		return exitOK
	}
	opt, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "czip: %v\nTry 'czip -h' for usage.\n", err)
		return exitError
	}
	if opt.command == "a" {
		return runAdd(opt, stdout, stderr)
	}

	a, err := openArchive(opt.archive)
	if err != nil {
		fmt.Fprintf(stderr, "czip: %v\n", err)
		return exitError
	}
	defer a.Close()
	fmt.Fprintf(stdout, "Archive: %s\n", a.describe())

	var res opResult
	switch opt.command {
	case "l":
		res = listArchive(a, opt.args, stdout)
	case "t":
		res = testArchive(a, opt.args, stdout, stderr)
		if res.errors == 0 {
			fmt.Fprintf(stdout, "No errors: %d file(s), %d bytes\n", res.files, res.bytes)
		} else {
			fmt.Fprintf(stdout, "%d error(s), %d file(s) OK\n", res.errors, res.files)
		}
	default:
		res = extractArchive(a, extractOptions{
			outDir:    opt.outDir,
			overwrite: opt.overwrite,
			flat:      opt.command == "e",
			patterns:  opt.args,
		}, stdout, stderr)
		fmt.Fprintf(stdout, "Extracted %d file(s), %d dir(s), %d bytes", res.files, res.dirs, res.bytes)
		if res.warnings > 0 || res.errors > 0 {
			fmt.Fprintf(stdout, "; %d skipped, %d failed", res.warnings, res.errors)
		}
		fmt.Fprintln(stdout)
	}
	if len(opt.args) > 0 && res.matched == 0 {
		// 패턴 오타로 아무 일도 안 했는데 성공처럼 끝나는 것을 막는다.
		fmt.Fprintln(stderr, "czip: warning: no entries match the given pattern(s)")
		res.warnings++
	}
	return exitCode(res.warnings, res.errors)
}

func runAdd(opt options, stdout, stderr io.Writer) int {
	res, err := createArchive(addOptions{
		archive:    opt.archive,
		inputs:     opt.args,
		volumeSize: opt.volumeSize,
		level:      opt.level,
		overwrite:  opt.overwrite,
	}, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "czip: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "%d file(s), %d dir(s): %d -> %d bytes (%s saved)\n",
		res.files, res.dirs, res.inBytes, res.outBytes, savedPercent(res.outBytes, res.inBytes))
	if n := len(res.volumes); n > 1 {
		fmt.Fprintf(stdout, "Created %s .. %s (%d volumes)\n", res.volumes[0], filepath.Base(res.volumes[n-1]), n)
	} else {
		fmt.Fprintf(stdout, "Created %s\n", res.volumes[0])
	}
	return exitCode(res.warnings, 0)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
