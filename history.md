### [2026-11-10 01:31] miniserver: --stop으로 백그라운드 서버 종료
- **기획:** --stop이 127.0.0.1:PORT로 전용 STOP 요청 전송, 서버는 루프백만 허용(LAN 403), OPTIONS 405로 웹페이지발 요청 차단
- **TC:** 정상: --stop 파싱·-p 조합, STOP→200+종료, 클라이언트 성공 / 엣지: -d·-s 무시, 루프백 판정(v4/v6/mapped), 서버 없음, 타 프로그램, 무응답 타임아웃
- **개발:** src/miniserver/src/main.rs, src/miniserver/README.md
- **검증:** 37 passed, 0 failed (clippy·rustfmt 경고 없음, 실제 백그라운드 실행→--stop 종료 확인)

### [2026-11-10 01:27] miniserver: --silent 옵션으로 quit 입력 비활성화
- **기획:** -s/--silent 지정 시 stdin 감시 스레드를 띄우지 않고 quit 안내 줄도 숨김(배너는 유지)
- **TC:** 정상: --silent/-s, 다른 옵션과 순서 무관 조합, 중복 지정 / 엣지: 기본값 false, -s 뒤 값 거부, --SILENT·--silent=1 거부, 안내 숨김, 도움말 표기
- **개발:** src/miniserver/src/main.rs, src/miniserver/README.md
- **검증:** 27 passed, 0 failed (clippy·rustfmt 경고 없음, 실제 실행에서 quit 무시 확인)

### [2026-11-10 00:08] get_size 빌드 바이너리 gitignore 처리
- **기획:** build.sh가 만드는 src/get_size/getsize가 untracked로 남아 루트 .gitignore에 기존 형식대로 추가
- **TC:** git check-ignore 추가 전 미적용(1) → 추가 후 적용(0)
- **개발:** .gitignore
- **검증:** check-ignore 통과, git status에서 getsize 사라짐 확인

### [2026-11-10 00:04] 루트 README에 get_size 항목 추가
- **기획:** 기존 형식에 맞춰 Get Size 섹션을 Mini Server 앞(추가 순서)에 삽입, 옵션은 get_size README 기준
- **TC:** grep '^## Get Size' 추가 전 0 → 추가 후 1
- **개발:** README.md
- **검증:** grep 확인 통과, CRLF 0, 기존 내용 변경 없음

### [2026-11-10 00:04] 루트 README에 miniserver 항목 추가
- **기획:** 기존 형식(제목·한 줄 설명·> 사용 예)에 맞춰 Mini Server 섹션을 끝에 추가
- **TC:** grep '^## Mini Server' 추가 전 0 → 추가 후 1
- **개발:** README.md
- **검증:** grep 확인 통과, CRLF 0, 기존 내용 변경 없음

### [2026-11-09 23:57] miniserver: 포트 충돌 시 친절한 안내 후 종료
- **기획:** 포트 사용 중/권한 거부 시 OS 에러 원문 대신 한 줄 해결 안내 출력, 종료 코드 1 유지
- **TC:** 정상: 실제 이중 바인딩 충돌, 8811→8812 추천 / 엣지: 65535→65534, 포트 1, 권한 거부, 기타 에러 원문 유지
- **개발:** src/miniserver/src/main.rs, src/miniserver/README.md
- **검증:** 20 passed, 0 failed / clippy 경고 0 / 실서버 이중 실행 시 안내 출력·기존 서버 유지 확인

### [2026-11-09 23:54] miniserver: Rust 초간단 정적 웹서버 추가
- **기획:** std만 쓰는 정적 서버, -p 포트(기본 8811)·-d 루트(기본 현재 폴더), 0.0.0.0 바인딩, stdin quit 종료
- **TC:** 정상: / → index.html, css/한글 파일, HEAD, 하위폴더 / 엣지: 포트 0·65536·누락, ../·%2e%2e·역슬래시, 405, 빈 연결
- **개발:** src/miniserver/src/main.rs, Cargo.toml, build.sh, README.md, .gitignore
- **검증:** 16 passed, 0 failed / clippy 경고 0 / 실서버 curl 및 quit 종료 확인
- **비고:** Termux Rust는 PRoot의 cc로 링크 실패 → build.sh에서 Termux clang 링커 지정
