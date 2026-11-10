# miniserver

A tiny static web server written in Rust (standard library only, no dependencies).

## Build

```sh
./build.sh
```

This creates the `miniserver` executable in this directory. You can also build directly with `cargo build --release`.

## Usage

```text
miniserver [-p PORT] [-d DIR] [-s]
```

- `-p PORT`: listen port (1-65535). Default `8811`.
- `-d DIR`: document root. Default is the current directory.
- `-s`, `--silent`: ignore the `quit` command (stdin is not read). Stop the server with Ctrl+C or `kill`. Useful when running in the background.
- `-h`: show help.

Requesting `/` (or any folder) serves that folder's `index.html`. Other files (CSS, JS, images, ...) are served with a Content-Type based on their extension.

The server listens on `0.0.0.0`, so other devices on the same network can connect. The startup message prints both the local and the network address.

Type `quit` and press Enter to stop the server (unless `--silent` is given).

```text
$ cd mysite && miniserver
miniserver 0.1.0 serving /home/me/mysite
  local:   http://localhost:8811/
  network: http://192.168.0.10:8811/
Type 'quit' and press Enter to stop.
```

Notes:

- Only `GET` and `HEAD` are supported. Other methods get `405`.
- Paths containing `..` (including `%2e%2e`) are rejected with `400`.
- A folder requested without a trailing slash is redirected (`301`) to `folder/`.
- Percent-encoded (e.g. Korean) file names are supported.
- If the port is already in use, it prints a one-line hint with another port and exits (code 1).
