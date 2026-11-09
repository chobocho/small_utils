// 초간단 정적 웹서버: 외부 크레이트 없이 std만 사용한다.
// 연결마다 스레드 하나를 띄우는 단순 구조 — 개인/로컬 용도라 동시 접속 수가 적다는 전제.
use std::fs;
use std::io::{self, BufRead, BufReader, Write};
use std::net::{TcpListener, TcpStream, UdpSocket};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

const DEFAULT_PORT: u16 = 8811;
const VERSION: &str = env!("CARGO_PKG_VERSION");

#[derive(Debug, PartialEq)]
pub struct Config {
    pub port: u16,
    pub root: PathBuf,
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
         Usage: miniserver [-p PORT] [-d DIR]\n\
         \n  -p PORT   listen port (1-65535, default {DEFAULT_PORT})\
         \n  -d DIR    document root (default: current directory)\
         \n  -h        show this help\n\
         \nType 'quit' and press Enter to stop the server."
    )
}

/// Ok(None)은 도움말 요청 — 호출 측에서 usage를 찍고 정상 종료한다.
pub fn parse_args(args: &[String]) -> Result<Option<Config>, String> {
    let mut cfg = Config {
        port: DEFAULT_PORT,
        root: PathBuf::from("."),
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
            "-h" | "--help" => return Ok(None),
            other => return Err(format!("unknown argument: '{other}'")),
        }
    }
    Ok(Some(cfg))
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
pub fn handle_connection(mut stream: TcpStream, root: &Path) {
    // 느린/멈춘 클라이언트가 스레드를 영원히 잡아두지 않도록 타임아웃
    let _ = stream.set_read_timeout(Some(Duration::from_secs(10)));
    let _ = serve_one(&mut stream, root);
}

fn serve_one(stream: &mut TcpStream, root: &Path) -> io::Result<()> {
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
    if !cfg.root.is_dir() {
        eprintln!("error: not a directory: {}", cfg.root.display());
        std::process::exit(2);
    }
    let root = Arc::new(cfg.root.canonicalize().unwrap_or(cfg.root));

    // 0.0.0.0 바인딩: 같은 네트워크의 다른 기기(폰 등)에서도 접속 가능
    let listener = match TcpListener::bind(("0.0.0.0", cfg.port)) {
        Ok(l) => l,
        Err(e) => {
            eprintln!("error: cannot listen on port {}: {e}", cfg.port);
            std::process::exit(1);
        }
    };

    println!("miniserver {VERSION} serving {}", root.display());
    println!("  local:   http://localhost:{}/", cfg.port);
    if let Some(ip) = lan_ip() {
        println!("  network: http://{ip}:{}/", cfg.port);
    }
    println!("Type 'quit' and press Enter to stop.");

    // stdin 감시 스레드: quit이면 즉시 종료.
    // EOF(백그라운드 실행 등 stdin 없음)에서는 종료하지 않고 감시만 멈춘다.
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

    for stream in listener.incoming() {
        match stream {
            Ok(s) => {
                let root = Arc::clone(&root);
                std::thread::spawn(move || handle_connection(s, &root));
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
                root: PathBuf::from("/tmp")
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
}
