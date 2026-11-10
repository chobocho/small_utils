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
miniserver --stop [-p PORT]
```

- `-p PORT`: listen port (1-65535). Default `8811`.
- `-d DIR`: document root. Default is the current directory.
- `-s`, `--silent`: ignore the `quit` command (stdin is not read). Stop the server with Ctrl+C or `kill`. Useful when running in the background.
- `--stop`: stop the miniserver running on this PC at `PORT` (default `8811`). `-d` and `-s` are ignored.
- `-h`: show help.

Requesting `/` (or any folder) serves that folder's `index.html`. Other files (CSS, JS, images, ...) are served with a Content-Type based on their extension.

The server listens on `0.0.0.0`, so other devices on the same network can connect. The startup message prints both the local and the network address.

Type `quit` and press Enter to stop the server (unless `--silent` is given).

To run in the background and stop it later:

```text
$ miniserver -s -p 8811 &
$ miniserver --stop -p 8811
Stopped miniserver on port 8811
```

`--stop` only works from the same PC: a stop request from another device gets `403`. If nothing is listening on the port, or another program owns it, a one-line message is printed and the exit code is 1.

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
