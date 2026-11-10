// 초간단 정적 웹서버: 외부 크레이트 없이 std만 사용한다.
// 연결마다 스레드 하나를 띄우는 단순 구조 — 개인/로컬 용도라 동시 접속 수가 적다는 전제.
use std::fs;
use std::io::{self, BufRead, BufReader, Read, Write};
use std::net::{IpAddr, SocketAddr, TcpListener, TcpStream, UdpSocket};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

const DEFAULT_PORT: u16 = 8811;
const VERSION: &str = env!("CARGO_PKG_VERSION");
// STOP 응답 본문 — 클라이언트가 상대가 miniserver인지 확인하는 표식
pub const STOP_ACK: &str = "miniserver: stopping\n";

#[derive(Debug, PartialEq)]
pub struct Config {
    pub port: u16,
    pub root: PathBuf,
    // true면 stdin의 quit 입력을 받지 않는다 — 백그라운드/서비스 실행용
    pub silent: bool,
    // true면 서버를 띄우지 않고 같은 PC에서 실행 중인 서버(-p 포트)에 종료 요청만 보낸다
    pub stop: bool,
}

#[derive(Debug, PartialEq)]
pub enum Resolved {
    File(PathBuf),
    Redirect(String),
    NotFound,
    BadRequest,
}

fn usage() -> String {
    format!(
        "miniserver {VERSION}\n\
         Usage: miniserver [-p PORT] [-d DIR] [-s]\n\
         \x20      miniserver --stop [-p PORT]\n\
         \n  -p PORT   listen port (1-65535, default {DEFAULT_PORT})\
         \n  -d DIR    document root (default: current directory)\
         \n  -s, --silent  ignore the 'quit' command (stop with Ctrl+C or kill)\
         \n  --stop    stop the miniserver running on this PC at PORT\
         \n  -h        show this help\n\
         \nType 'quit' and press Enter to stop the server."
    )
}

/// Ok(None)은 도움말 요청 — 호출 측에서 usage를 찍고 정상 종료한다.
pub fn parse_args(args: &[String]) -> Result<Option<Config>, String> {
    let mut cfg = Config {
        port: DEFAULT_PORT,
        root: PathBuf::from("."),
        silent: false,
        stop: false,
    };
    let mut it = args.iter();
    while let Some(a) = it.next() {
        match a.as_str() {
            "-p" => {
                let v = it.next().ok_or("-p requires a port number")?;
                // u16 파싱이 음수·65536 이상을 걸러주고, 0은 OS 임의 포트라 별도로 막는다
                cfg.port = match v.parse::<u16>() {
                    Ok(p) if p != 0 => p,
                    _ => return Err(format!("invalid port: '{v}' (1-65535)")),
                };
            }
            "-d" => {
                let v = it.next().ok_or("-d requires a directory")?;
                cfg.root = PathBuf::from(v);
            }
            "-s" | "--silent" => cfg.silent = true,
            "--stop" => cfg.stop = true,
            "-h" | "--help" => return Ok(None),
            other => return Err(format!("unknown argument: '{other}'")),
        }
    }
    Ok(Some(cfg))
}

/// 시작 메시지의 quit 안내 줄. silent면 quit을 받지 않으므로 안내도 하지 않는다.
pub fn quit_hint(silent: bool) -> Option<&'static str> {
    (!silent).then_some("Type 'quit' and press Enter to stop.")
}

/// STOP 요청을 받아줄 상대인지 판정한다.
/// 0.0.0.0에 바인딩해 LAN에도 열려 있으므로, 다른 기기가 서버를 끄지 못하게 루프백만 허용한다.
/// IPv4-mapped IPv6(::ffff:127.0.0.1)도 같은 PC이므로 정규화한 뒤 판정한다.
pub fn stop_allowed(peer: IpAddr) -> bool {
    peer.to_canonical().is_loopback()
}

/// 실행 중인 서버에 STOP 요청을 보내고 결과를 한 줄 메시지로 돌려준다.
/// 응답 본문이 STOP_ACK로 끝날 때만 성공으로 본다 — 같은 포트의 다른 프로그램과 구분하기 위함.
pub fn stop_server(addr: SocketAddr, timeout: Duration) -> Result<String, String> {
    let port = addr.port();
    let mut c = match TcpStream::connect_timeout(&addr, timeout) {
        Ok(c) => c,
        Err(e) if e.kind() == io::ErrorKind::ConnectionRefused => {
            return Err(format!("No miniserver is running on port {port}."))
        }
        Err(e) => return Err(format!("error: cannot connect to port {port}: {e}")),
    };
    let io_err = |e: io::Error| format!("error: no valid response from port {port}: {e}");
    c.set_read_timeout(Some(timeout)).map_err(io_err)?;
    c.set_write_timeout(Some(timeout)).map_err(io_err)?;
    c.write_all(b"STOP / HTTP/1.1\r\nHost: localhost\r\n\r\n")
        .map_err(io_err)?;
    // 정상 응답은 수십 바이트 — 상한을 둬서 엉뚱한 프로그램이 대량 데이터를 보내도 메모리를 지킨다
    let mut buf = Vec::new();
    (&mut c).take(4096).read_to_end(&mut buf).map_err(io_err)?;
    let resp = String::from_utf8_lossy(&buf);
    if resp.starts_with("HTTP/1.1 200 ") && resp.ends_with(STOP_ACK) {
        Ok(format!("Stopped miniserver on port {port}"))
    } else {
        Err(format!(
            "Port {port} is used by another program, not miniserver."
        ))
    }
}

pub fn is_quit(line: &str) -> bool {
    line.trim().eq_ignore_ascii_case("quit")
}

/// URL의 %XX를 풀어 UTF-8 문자열로 만든다. 형식 오류나 잘못된 UTF-8이면 None.
/// O(n) time, O(n) extra space
pub fn percent_decode(s: &str) -> Option<String> {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%' {
            let hex = b.get(i + 1..i + 3)?;
            let hex = std::str::from_utf8(hex).ok()?;
            out.push(u8::from_str_radix(hex, 16).ok()?);
            i += 3;
        } else {
            out.push(b[i]);
            i += 1;
        }
    }
    String::from_utf8(out).ok()
}

pub fn content_type(path: &Path) -> &'static str {
    let ext = path
        .extension()
        .and_then(|e| e.to_str())
        .map(|e| e.to_ascii_lowercase())
        .unwrap_or_default();
    match ext.as_str() {
        "html" | "htm" => "text/html; charset=utf-8",
        "css" => "text/css; charset=utf-8",
        "js" | "mjs" => "text/javascript; charset=utf-8",
        "json" => "application/json",
        "txt" | "md" => "text/plain; charset=utf-8",
        "svg" => "image/svg+xml",
        "png" => "image/png",
        "jpg" | "jpeg" => "image/jpeg",
        "gif" => "image/gif",
        "webp" => "image/webp",
        "ico" => "image/x-icon",
        "wasm" => "application/wasm",
        "pdf" => "application/pdf",
        "mp4" => "video/mp4",
        "mp3" => "audio/mpeg",
        "woff" => "font/woff",
        "woff2" => "font/woff2",
        _ => "application/octet-stream",
    }
}

/// 요청 대상(target)을 root 아래의 실제 파일로 바꾼다.
/// 보안: 디코딩 *후*에 `..`를 검사해야 `%2e%2e` 우회를 막을 수 있다.
/// 역슬래시·NUL은 Windows 경로 구분자 우회 방지를 위해 거부한다.
/// (root 안의 심볼릭 링크가 밖을 가리키는 경우는 막지 않는다 — 로컬 용도 전제)
pub fn resolve(root: &Path, target: &str) -> Resolved {
    let path = target.split(['?', '#']).next().unwrap_or("");
    if !path.starts_with('/') {
        return Resolved::BadRequest;
    }
    let Some(decoded) = percent_decode(path) else {
        return Resolved::BadRequest;
    };

    let mut fs_path = root.to_path_buf();
    for seg in decoded.split('/') {
        if seg == ".." || seg.contains('\\') || seg.contains('\0') {
            return Resolved::BadRequest;
        }
        if !seg.is_empty() && seg != "." {
            fs_path.push(seg);
        }
    }

    if fs_path.is_dir() {
        // 슬래시 없이 폴더에 들어오면 하위 index.html의 상대 링크가 깨지므로 리다이렉트
        if !decoded.ends_with('/') {
            return Resolved::Redirect(format!("{path}/"));
        }
        fs_path.push("index.html");
    }
    if fs_path.is_file() {
        Resolved::File(fs_path)
    } else {
        Resolved::NotFound
    }
}

fn write_response(
    stream: &mut TcpStream,
    status: &str,
    headers: &[(&str, &str)],
    body: &[u8],
    send_body: bool,
) -> io::Result<()> {
    let mut head = format!("HTTP/1.1 {status}\r\n");
    for (k, v) in headers {
        head.push_str(&format!("{k}: {v}\r\n"));
    }
    head.push_str(&format!(
        "Content-Length: {}\r\nConnection: close\r\n\r\n",
        body.len()
    ));
    stream.write_all(head.as_bytes())?;
    if send_body {
        stream.write_all(body)?;
    }
    stream.flush()
}

fn error_response(stream: &mut TcpStream, status: &str, send_body: bool) -> io::Result<()> {
    let body = format!("{status}\n");
    write_response(
        stream,
        status,
        &[("Content-Type", "text/plain; charset=utf-8")],
        body.as_bytes(),
        send_body,
    )
}

/// 요청 하나를 처리하고 연결을 닫는다 (keep-alive 미지원 — 단순성 우선).
/// 허용된 STOP 요청이었으면 true — 실제 종료는 호출 측(main)이 한다(테스트에서 프로세스를 죽이지 않도록).
pub fn handle_connection(mut stream: TcpStream, root: &Path) -> bool {
    // 느린/멈춘 클라이언트가 스레드를 영원히 잡아두지 않도록 타임아웃
    let _ = stream.set_read_timeout(Some(Duration::from_secs(10)));
    let mut stop_requested = false;
    let _ = serve_one(&mut stream, root, &mut stop_requested);
    stop_requested
}

fn serve_one(stream: &mut TcpStream, root: &Path, stop_requested: &mut bool) -> io::Result<()> {
    let mut reader = BufReader::new(stream.try_clone()?);
    let mut request_line = String::new();
    if reader.read_line(&mut request_line)? == 0 {
        return Ok(()); // 아무것도 안 보내고 닫힌 연결
    }
    // 헤더는 쓰지 않지만 읽어서 비워야 클라이언트 쪽에서 RST가 나지 않는다
    let mut line = String::new();
    loop {
        line.clear();
        if reader.read_line(&mut line)? == 0 || line == "\r\n" || line == "\n" {
            break;
        }
    }

    let parts: Vec<&str> = request_line.split_whitespace().collect();
    let [method, target, version] = parts[..] else {
        return error_response(stream, "400 Bad Request", true);
    };
    if !version.starts_with("HTTP/") {
        return error_response(stream, "400 Bad Request", true);
    }
    // 전용 STOP 메서드: GET이 아니므로 브라우저 웹페이지가 보내려면 CORS 프리플라이트(OPTIONS)가
    // 필요한데, OPTIONS는 아래에서 405로 거부되어 웹페이지발 종료 요청이 차단된다.
    if method == "STOP" {
        let allowed = stream.peer_addr().is_ok_and(|a| stop_allowed(a.ip()));
        if !allowed {
            return error_response(stream, "403 Forbidden", true);
        }
        write_response(
            stream,
            "200 OK",
            &[("Content-Type", "text/plain; charset=utf-8")],
            STOP_ACK.as_bytes(),
            true,
        )?;
        *stop_requested = true;
        return Ok(());
    }
    let send_body = match method {
        "GET" => true,
        "HEAD" => false,
        _ => {
            return write_response(
                stream,
                "405 Method Not Allowed",
                &[
                    ("Allow", "GET, HEAD"),
                    ("Content-Type", "text/plain; charset=utf-8"),
                ],
                b"405 Method Not Allowed\n",
                true,
            )
        }
    };

    match resolve(root, target) {
        Resolved::File(p) => match fs::read(&p) {
            Ok(data) => write_response(
                stream,
                "200 OK",
                &[("Content-Type", content_type(&p))],
                &data,
                send_body,
            ),
            Err(_) => error_response(stream, "403 Forbidden", send_body),
        },
        Resolved::Redirect(loc) => write_response(
            stream,
            "301 Moved Permanently",
            &[("Location", &loc)],
            b"",
            send_body,
        ),
        Resolved::NotFound => error_response(stream, "404 Not Found", send_body),
        Resolved::BadRequest => error_response(stream, "400 Bad Request", send_body),
    }
}

/// 바인딩 실패를 사용자 친화적인 한 줄 안내로 바꾼다.
/// 흔한 원인(포트 사용 중·권한 없음)은 OS 에러 문구 대신 해결책을 보여주고,
/// 예상 못 한 에러만 원문을 남겨 디버깅 단서를 잃지 않게 한다.
pub fn bind_error_message(port: u16, e: &io::Error) -> String {
    match e.kind() {
        io::ErrorKind::AddrInUse => {
            // 65535에서 +1은 u16 범위를 넘으므로 바로 아래 포트를 추천
            let next = port.checked_add(1).unwrap_or(port - 1);
            format!("Port {port} is already in use. Try another port: miniserver -p {next}")
        }
        io::ErrorKind::PermissionDenied => format!(
            "Permission denied for port {port}. Use a port above 1023: miniserver -p {DEFAULT_PORT}"
        ),
        _ => format!("error: cannot listen on port {port}: {e}"),
    }
}

/// 다른 기기에서 접속할 주소를 보여주기 위한 LAN IP 추정.
/// UDP connect는 라우팅 테이블만 조회하고 실제 패킷은 보내지 않는다.
fn lan_ip() -> Option<String> {
    let sock = UdpSocket::bind("0.0.0.0:0").ok()?;
    sock.connect("8.8.8.8:80").ok()?;
    let ip = sock.local_addr().ok()?.ip();
    (!ip.is_loopback() && !ip.is_unspecified()).then(|| ip.to_string())
}

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let cfg = match parse_args(&args) {
        Ok(Some(c)) => c,
        Ok(None) => {
            println!("{}", usage());
            return;
        }
        Err(e) => {
            eprintln!("error: {e}\n\n{}", usage());
            std::process::exit(2);
        }
    };
    // --stop은 서버를 띄우지 않으므로 -d/-s는 의미가 없어 무시한다 (root 검사보다 먼저 처리)
    if cfg.stop {
        let addr = SocketAddr::from(([127, 0, 0, 1], cfg.port));
        match stop_server(addr, Duration::from_secs(3)) {
            Ok(msg) => {
                println!("{msg}");
                return;
            }
            Err(e) => {
                eprintln!("{e}");
                std::process::exit(1);
            }
        }
    }
    if !cfg.root.is_dir() {
        eprintln!("error: not a directory: {}", cfg.root.display());
        std::process::exit(2);
    }
    let root = Arc::new(cfg.root.canonicalize().unwrap_or(cfg.root));

    // 0.0.0.0 바인딩: 같은 네트워크의 다른 기기(폰 등)에서도 접속 가능
    let listener = match TcpListener::bind(("0.0.0.0", cfg.port)) {
        Ok(l) => l,
        Err(e) => {
            eprintln!("{}", bind_error_message(cfg.port, &e));
            std::process::exit(1);
        }
    };

    println!("miniserver {VERSION} serving {}", root.display());
    println!("  local:   http://localhost:{}/", cfg.port);
    if let Some(ip) = lan_ip() {
        println!("  network: http://{ip}:{}/", cfg.port);
    }
    if let Some(hint) = quit_hint(cfg.silent) {
        println!("{hint}");
    }

    // stdin 감시 스레드: quit이면 즉시 종료.
    // EOF(백그라운드 실행 등 stdin 없음)에서는 종료하지 않고 감시만 멈춘다.
    // silent면 스레드 자체를 띄우지 않아 stdin을 전혀 읽지 않는다.
    if !cfg.silent {
        std::thread::spawn(|| {
            for line in io::stdin().lock().lines() {
                match line {
                    Ok(l) if is_quit(&l) => {
                        println!("Bye.");
                        std::process::exit(0);
                    }
                    Ok(_) => {}
                    Err(_) => break,
                }
            }
        });
    }

    for stream in listener.incoming() {
        match stream {
            Ok(s) => {
                let root = Arc::clone(&root);
                std::thread::spawn(move || {
                    if handle_connection(s, &root) {
                        println!("Stopped by --stop request. Bye.");
                        std::process::exit(0);
                    }
                });
            }
            Err(e) => eprintln!("accept error: {e}"),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::io::{Read, Write};
    use std::net::{TcpListener, TcpStream};
    use std::sync::atomic::{AtomicUsize, Ordering};

    fn s(v: &[&str]) -> Vec<String> {
        v.iter().map(|x| x.to_string()).collect()
    }

    // 테스트마다 독립된 임시 폴더를 만든다 (외부 크레이트 없이)
    fn temp_root() -> PathBuf {
        static N: AtomicUsize = AtomicUsize::new(0);
        let dir = std::env::temp_dir().join(format!(
            "miniserver-test-{}-{}",
            std::process::id(),
            N.fetch_add(1, Ordering::SeqCst)
        ));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(dir.join("sub")).unwrap();
        fs::write(dir.join("index.html"), "<h1>root</h1>").unwrap();
        fs::write(dir.join("style.css"), "body{}").unwrap();
        fs::write(dir.join("sub").join("index.html"), "<h1>sub</h1>").unwrap();
        fs::write(dir.join("한글.txt"), "hangul").unwrap();
        dir
    }

    // ---- 인자 파싱 ----
    #[test]
    fn args_default_port_and_cwd() {
        let c = parse_args(&s(&[])).unwrap().unwrap();
        assert_eq!(c.port, 8811);
        assert_eq!(c.root, PathBuf::from("."));
    }

    #[test]
    fn args_port_and_dir() {
        let c = parse_args(&s(&["-p", "9000", "-d", "/tmp"]))
            .unwrap()
            .unwrap();
        assert_eq!(
            c,
            Config {
                port: 9000,
                root: PathBuf::from("/tmp"),
                silent: false,
                stop: false,
            }
        );
    }

    #[test]
    fn args_port_boundaries() {
        assert_eq!(parse_args(&s(&["-p", "1"])).unwrap().unwrap().port, 1);
        assert_eq!(
            parse_args(&s(&["-p", "65535"])).unwrap().unwrap().port,
            65535
        );
        assert!(parse_args(&s(&["-p", "0"])).is_err());
        assert!(parse_args(&s(&["-p", "65536"])).is_err());
        assert!(parse_args(&s(&["-p", "-1"])).is_err());
        assert!(parse_args(&s(&["-p", "abc"])).is_err());
        assert!(parse_args(&s(&["-p", ""])).is_err());
    }

    #[test]
    fn args_missing_value_and_unknown() {
        assert!(parse_args(&s(&["-p"])).is_err());
        assert!(parse_args(&s(&["-d"])).is_err());
        assert!(parse_args(&s(&["-x"])).is_err());
        assert!(parse_args(&s(&["extra"])).is_err());
    }

    #[test]
    fn args_help_returns_none() {
        assert_eq!(parse_args(&s(&["-h"])).unwrap(), None);
        assert_eq!(parse_args(&s(&["--help"])).unwrap(), None);
    }

    #[test]
    fn args_silent_default_false() {
        assert!(!parse_args(&s(&[])).unwrap().unwrap().silent);
        assert!(!parse_args(&s(&["-p", "9000"])).unwrap().unwrap().silent);
    }

    #[test]
    fn args_silent_long_and_short() {
        assert!(parse_args(&s(&["--silent"])).unwrap().unwrap().silent);
        assert!(parse_args(&s(&["-s"])).unwrap().unwrap().silent);
    }

    #[test]
    fn args_silent_with_other_options_any_order() {
        let c = parse_args(&s(&["-p", "9000", "--silent", "-d", "/tmp"]))
            .unwrap()
            .unwrap();
        assert_eq!(
            c,
            Config {
                port: 9000,
                root: PathBuf::from("/tmp"),
                silent: true,
                stop: false,
            }
        );
        let c = parse_args(&s(&["-s", "-p", "1"])).unwrap().unwrap();
        assert!(c.silent);
        assert_eq!(c.port, 1);
    }

    #[test]
    fn args_silent_repeated_is_ok() {
        assert!(parse_args(&s(&["-s", "--silent"])).unwrap().unwrap().silent);
    }

    #[test]
    fn args_silent_does_not_swallow_value_or_accept_variants() {
        // -s는 값을 받지 않으므로 뒤의 토큰은 별도 인자로 해석되어야 한다
        assert!(parse_args(&s(&["-s", "9000"])).is_err());
        // -p 값 자리에 -s가 오면 포트 오류
        assert!(parse_args(&s(&["-p", "-s"])).is_err());
        assert!(parse_args(&s(&["--SILENT"])).is_err());
        assert!(parse_args(&s(&["-silent"])).is_err());
        assert!(parse_args(&s(&["--silent=1"])).is_err());
    }

    #[test]
    fn quit_hint_hidden_when_silent() {
        assert!(quit_hint(false).unwrap().contains("quit"));
        assert_eq!(quit_hint(true), None);
    }

    #[test]
    fn usage_mentions_silent() {
        let u = usage();
        assert!(u.contains("--silent"));
        assert!(u.contains("-s"));
    }

    // ---- quit 판정 ----
    #[test]
    fn quit_detection() {
        assert!(is_quit("quit"));
        assert!(is_quit("  quit \r\n"));
        assert!(is_quit("QUIT"));
        assert!(!is_quit(""));
        assert!(!is_quit("quit now"));
        assert!(!is_quit("q"));
    }

    // ---- 퍼센트 디코딩 ----
    #[test]
    fn decode_cases() {
        assert_eq!(percent_decode("/a%20b").as_deref(), Some("/a b"));
        assert_eq!(percent_decode("/%ED%95%9C").as_deref(), Some("/한"));
        assert_eq!(percent_decode("/plain").as_deref(), Some("/plain"));
        assert_eq!(percent_decode("/bad%2"), None);
        assert_eq!(percent_decode("/bad%zz"), None);
        assert_eq!(percent_decode("/%FF"), None); // 잘못된 UTF-8
    }

    // ---- Content-Type ----
    #[test]
    fn content_types() {
        assert_eq!(
            content_type(Path::new("a.html")),
            "text/html; charset=utf-8"
        );
        assert_eq!(content_type(Path::new("a.CSS")), "text/css; charset=utf-8");
        assert_eq!(
            content_type(Path::new("a.js")),
            "text/javascript; charset=utf-8"
        );
        assert_eq!(content_type(Path::new("a.png")), "image/png");
        assert_eq!(content_type(Path::new("noext")), "application/octet-stream");
    }

    // ---- 경로 해석 ----
    #[test]
    fn resolve_root_gives_index() {
        let r = temp_root();
        assert_eq!(resolve(&r, "/"), Resolved::File(r.join("index.html")));
        assert_eq!(resolve(&r, "/?x=1#y"), Resolved::File(r.join("index.html")));
    }

    #[test]
    fn resolve_files_and_dirs() {
        let r = temp_root();
        assert_eq!(
            resolve(&r, "/style.css"),
            Resolved::File(r.join("style.css"))
        );
        assert_eq!(
            resolve(&r, "/sub/"),
            Resolved::File(r.join("sub").join("index.html"))
        );
        assert_eq!(resolve(&r, "/sub"), Resolved::Redirect("/sub/".into()));
        assert_eq!(
            resolve(&r, "/%ED%95%9C%EA%B8%80.txt"),
            Resolved::File(r.join("한글.txt"))
        );
    }

    #[test]
    fn resolve_not_found() {
        let r = temp_root();
        assert_eq!(resolve(&r, "/nope.html"), Resolved::NotFound);
        fs::remove_file(r.join("index.html")).unwrap();
        assert_eq!(resolve(&r, "/"), Resolved::NotFound);
    }

    #[test]
    fn resolve_rejects_traversal_and_garbage() {
        let r = temp_root();
        assert_eq!(resolve(&r, "/../etc/passwd"), Resolved::BadRequest);
        assert_eq!(resolve(&r, "/sub/../../x"), Resolved::BadRequest);
        assert_eq!(resolve(&r, "/%2e%2e/x"), Resolved::BadRequest);
        assert_eq!(resolve(&r, "/..%5cx"), Resolved::BadRequest);
        assert_eq!(resolve(&r, "/bad%zz"), Resolved::BadRequest);
        assert_eq!(resolve(&r, "no-slash"), Resolved::BadRequest);
        assert_eq!(resolve(&r, ""), Resolved::BadRequest);
    }

    // ---- 실제 소켓으로 요청/응답 ----
    fn request(root: &Path, raw: &str) -> String {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        let root = root.to_path_buf();
        let h = std::thread::spawn(move || {
            let (stream, _) = listener.accept().unwrap();
            handle_connection(stream, &root);
        });
        let mut c = TcpStream::connect(addr).unwrap();
        c.write_all(raw.as_bytes()).unwrap();
        let mut out = String::new();
        c.read_to_string(&mut out).unwrap();
        h.join().unwrap();
        out
    }

    #[test]
    fn http_get_index() {
        let r = temp_root();
        let out = request(&r, "GET / HTTP/1.1\r\nHost: x\r\n\r\n");
        assert!(out.starts_with("HTTP/1.1 200 OK\r\n"), "{out}");
        assert!(out.contains("Content-Type: text/html; charset=utf-8\r\n"));
        assert!(out.contains("Content-Length: 13\r\n"));
        assert!(out.ends_with("\r\n\r\n<h1>root</h1>"));
    }

    #[test]
    fn http_head_has_no_body() {
        let r = temp_root();
        let out = request(&r, "HEAD /style.css HTTP/1.1\r\n\r\n");
        assert!(out.starts_with("HTTP/1.1 200 OK\r\n"), "{out}");
        assert!(out.contains("Content-Length: 6\r\n"));
        assert!(out.ends_with("\r\n\r\n"));
    }

    #[test]
    fn http_error_statuses() {
        let r = temp_root();
        assert!(request(&r, "GET /nope HTTP/1.1\r\n\r\n").starts_with("HTTP/1.1 404 "));
        assert!(request(&r, "GET /../x HTTP/1.1\r\n\r\n").starts_with("HTTP/1.1 400 "));
        assert!(request(&r, "POST / HTTP/1.1\r\n\r\n").starts_with("HTTP/1.1 405 "));
        assert!(request(&r, "garbage\r\n\r\n").starts_with("HTTP/1.1 400 "));
        let redir = request(&r, "GET /sub HTTP/1.1\r\n\r\n");
        assert!(redir.starts_with("HTTP/1.1 301 "), "{redir}");
        assert!(redir.contains("Location: /sub/\r\n"));
    }

    #[test]
    fn http_empty_connection_does_not_panic() {
        let r = temp_root();
        // 아무것도 보내지 않고 닫는 클라이언트 (브라우저 프리커넥트 등)
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        let h = std::thread::spawn(move || {
            let (stream, _) = listener.accept().unwrap();
            handle_connection(stream, &r);
        });
        drop(TcpStream::connect(addr).unwrap());
        h.join().unwrap();
    }

    // ---- --stop: 백그라운드 서버 종료 ----
    #[test]
    fn args_stop_flag() {
        assert!(!parse_args(&s(&[])).unwrap().unwrap().stop);
        let c = parse_args(&s(&["--stop"])).unwrap().unwrap();
        assert!(c.stop);
        assert_eq!(c.port, 8811);
        let c = parse_args(&s(&["-p", "9000", "--stop"])).unwrap().unwrap();
        assert!(c.stop);
        assert_eq!(c.port, 9000);
        assert_eq!(
            parse_args(&s(&["--stop", "-p", "9000"]))
                .unwrap()
                .unwrap()
                .port,
            9000
        );
    }

    #[test]
    fn args_stop_ignores_dir_and_silent_but_validates_port() {
        // -d/-s는 --stop과 함께 와도 파싱 오류가 아니다 (main에서 무시)
        let c = parse_args(&s(&["--stop", "-d", "/no/such/dir", "-s"]))
            .unwrap()
            .unwrap();
        assert!(c.stop);
        assert!(parse_args(&s(&["--stop", "-p", "0"])).is_err());
        assert!(parse_args(&s(&["--stop", "-p"])).is_err());
        assert!(parse_args(&s(&["--STOP"])).is_err());
        assert!(parse_args(&s(&["-stop"])).is_err());
        assert!(parse_args(&s(&["stop"])).is_err());
    }

    #[test]
    fn usage_mentions_stop() {
        assert!(usage().contains("--stop"));
    }

    #[test]
    fn stop_allowed_only_from_loopback() {
        let ip = |x: &str| x.parse::<IpAddr>().unwrap();
        assert!(stop_allowed(ip("127.0.0.1")));
        assert!(stop_allowed(ip("127.255.255.254")));
        assert!(stop_allowed(ip("::1")));
        assert!(stop_allowed(ip("::ffff:127.0.0.1")));
        assert!(!stop_allowed(ip("192.168.0.10")));
        assert!(!stop_allowed(ip("10.0.0.1")));
        assert!(!stop_allowed(ip("0.0.0.0")));
        assert!(!stop_allowed(ip("::")));
        assert!(!stop_allowed(ip("::ffff:192.168.0.10")));
    }

    // 요청 하나를 처리하고 (응답, 종료 요청 여부)를 돌려준다
    fn request_outcome(root: &Path, raw: &str) -> (String, bool) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        let root = root.to_path_buf();
        let h = std::thread::spawn(move || {
            let (stream, _) = listener.accept().unwrap();
            handle_connection(stream, &root)
        });
        let mut c = TcpStream::connect(addr).unwrap();
        c.write_all(raw.as_bytes()).unwrap();
        let mut out = String::new();
        c.read_to_string(&mut out).unwrap();
        (out, h.join().unwrap())
    }

    #[test]
    fn http_stop_from_loopback_requests_shutdown() {
        let r = temp_root();
        let (out, stop) = request_outcome(&r, "STOP / HTTP/1.1\r\nHost: x\r\n\r\n");
        assert!(out.starts_with("HTTP/1.1 200 OK\r\n"), "{out}");
        assert!(out.ends_with(STOP_ACK), "{out}");
        assert!(stop);
    }

    #[test]
    fn http_normal_requests_do_not_shutdown() {
        let r = temp_root();
        assert!(!request_outcome(&r, "GET / HTTP/1.1\r\n\r\n").1);
        assert!(!request_outcome(&r, "garbage\r\n\r\n").1);
        // 메서드는 대소문자 구분 — 소문자 stop은 일반 405
        let (out, stop) = request_outcome(&r, "stop / HTTP/1.1\r\n\r\n");
        assert!(out.starts_with("HTTP/1.1 405 "), "{out}");
        assert!(!stop);
        // 브라우저 CORS 프리플라이트(OPTIONS)가 거부되어야 웹페이지발 STOP이 막힌다
        let (out, stop) = request_outcome(&r, "OPTIONS / HTTP/1.1\r\n\r\n");
        assert!(out.starts_with("HTTP/1.1 405 "), "{out}");
        assert!(out.contains("Allow: GET, HEAD\r\n"), "{out}");
        assert!(!stop);
    }

    #[test]
    fn stop_server_success() {
        let r = temp_root();
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        let h = std::thread::spawn(move || {
            let (stream, _) = listener.accept().unwrap();
            handle_connection(stream, &r)
        });
        let msg = stop_server(addr, Duration::from_secs(2)).unwrap();
        assert_eq!(msg, format!("Stopped miniserver on port {}", addr.port()));
        assert!(h.join().unwrap());
    }

    #[test]
    fn stop_server_nothing_running() {
        // 포트를 잡았다 놓아 '아무도 듣지 않는' 포트를 만든다
        let addr = TcpListener::bind("127.0.0.1:0")
            .unwrap()
            .local_addr()
            .unwrap();
        let err = stop_server(addr, Duration::from_secs(2)).unwrap_err();
        assert_eq!(
            err,
            format!("No miniserver is running on port {}.", addr.port())
        );
    }

    // 임의 응답을 돌려주는 '다른 프로그램' 흉내
    fn fake_server(reply: &'static [u8]) -> (SocketAddr, std::thread::JoinHandle<()>) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        let h = std::thread::spawn(move || {
            let (mut c, _) = listener.accept().unwrap();
            let mut buf = [0u8; 256];
            let _ = c.read(&mut buf);
            let _ = c.write_all(reply);
        });
        (addr, h)
    }

    #[test]
    fn stop_server_other_program() {
        for reply in [
            &b"HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n"[..],
            &b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"[..],
            &b""[..],
            &b"SSH-2.0-OpenSSH\r\n"[..],
        ] {
            let (addr, h) = fake_server(reply);
            let err = stop_server(addr, Duration::from_secs(2)).unwrap_err();
            assert_eq!(
                err,
                format!(
                    "Port {} is used by another program, not miniserver.",
                    addr.port()
                ),
                "reply={reply:?}"
            );
            h.join().unwrap();
        }
    }

    #[test]
    fn stop_server_times_out_on_silent_peer() {
        // 접속은 받지만 아무 응답도 하지 않는 상대 — 무한 대기하면 안 된다
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        let h = std::thread::spawn(move || {
            let (c, _) = listener.accept().unwrap();
            std::thread::sleep(Duration::from_millis(800));
            drop(c);
        });
        let t = std::time::Instant::now();
        let err = stop_server(addr, Duration::from_millis(200)).unwrap_err();
        assert!(
            t.elapsed() < Duration::from_millis(700),
            "{:?}",
            t.elapsed()
        );
        assert!(err.starts_with("error: "), "{err}");
        h.join().unwrap();
    }

    // ---- 포트 바인딩 실패 안내 ----
    #[test]
    fn bind_in_use_real_conflict() {
        // 실제로 같은 포트를 두 번 열어 OS가 주는 에러 종류를 그대로 검증한다
        let first = TcpListener::bind("0.0.0.0:0").unwrap();
        let port = first.local_addr().unwrap().port();
        let err = TcpListener::bind(("0.0.0.0", port)).unwrap_err();
        let msg = bind_error_message(port, &err);
        let next = port.checked_add(1).unwrap_or(port - 1);
        assert_eq!(
            msg,
            format!("Port {port} is already in use. Try another port: miniserver -p {next}")
        );
        assert!(!msg.contains("os error"));
    }

    #[test]
    fn bind_in_use_suggests_neighbor_port() {
        let e = io::Error::from(io::ErrorKind::AddrInUse);
        assert_eq!(
            bind_error_message(8811, &e),
            "Port 8811 is already in use. Try another port: miniserver -p 8812"
        );
        // 65535 + 1은 넘치므로 아래 포트를 추천
        assert_eq!(
            bind_error_message(65535, &e),
            "Port 65535 is already in use. Try another port: miniserver -p 65534"
        );
        assert_eq!(
            bind_error_message(1, &e),
            "Port 1 is already in use. Try another port: miniserver -p 2"
        );
    }

    #[test]
    fn bind_permission_denied() {
        let e = io::Error::from(io::ErrorKind::PermissionDenied);
        assert_eq!(
            bind_error_message(80, &e),
            "Permission denied for port 80. Use a port above 1023: miniserver -p 8811"
        );
    }

    #[test]
    fn bind_other_error_keeps_detail() {
        let e = io::Error::other("boom");
        assert_eq!(
            bind_error_message(8811, &e),
            "error: cannot listen on port 8811: boom"
        );
    }
}
