# czip

A small Go ZIP archiver with split-volume support and ARJ-style commands. One binary creates, lists, tests, and extracts archives. It uses only the Go standard library.

## Build

```sh
./build.sh
```

This creates the `czip` executable in this directory. You can also build directly with `go build -o czip .`.

## Usage

```text
czip <command> [-switch...] <archive> [file|pattern...]
```

Commands:

- `a`: add files and directories to a new archive. Directories are always recursed.
- `x`: extract with full paths.
- `e`: extract without paths, placing every file in one directory.
- `l`: list contents.
- `t`: test integrity by decompressing every entry and checking its CRC.

Switches (written attached to their value, as in ARJ):

- `-v<size>`: split the archive into volumes of `<size>` (`a` only). Plain numbers are bytes; `K`, `M`, `G`, `T` are 1024-based, so `-v1440k`, `-v100m`, `-v1.5g` all work. The minimum is `64k`.
- `-m<0-9>`: compression level (`a` only). `0` stores without compression, `1` is fastest, `9` is smallest. The default is `6`.
- `-o<dir>`: output directory for `x` and `e`. The default is the current directory.
- `-y`: overwrite. For `x`/`e` it replaces existing files; for `a` it replaces an existing archive.
- `--`: treat every following argument as a name, so files that start with `-` can be added.

If the archive name given to `a` has no extension, `.zip` is appended.

Examples:

```sh
czip a backup.zip docs photos
czip a -v100m -m9 backup.zip photos
czip l backup.zip
czip t backup.zip
czip x -orestore backup.zip.001
czip e -oflat backup.zip '*.jpg'
```

## Split volumes

With `-v`, the archive is written as `<archive>.001`, `<archive>.002`, and so on. Every volume except the last is exactly the requested size. After `.999` the suffix grows to four digits.

The volumes are a normal ZIP file cut into pieces, which is the same scheme 7-Zip uses:

- `czip` and 7-Zip read each other's split sets directly.
- Joining the pieces gives a regular ZIP that any tool can open: `cat backup.zip.* > backup.zip`.

For `x`, `e`, `l`, and `t` you can name the set as `backup.zip`, as any one volume such as `backup.zip.003`, or as `backup`.

This is not the PKZIP `.z01`/`.z02` spanning format used by WinZip and `zip -s`; `czip` neither reads nor writes that format.

An archive name refers to either one file or one volume set, never both. Creating `backup.zip` with `-y` removes an older `backup.zip.001…` set, and the reverse.

A volume set is the unbroken run `.001`, `.002`, and so on. A numbered file outside that run, such as a dated copy named `backup.zip.20261119`, is not treated as a volume: it is never deleted, and it does not stop the set from being read. Because the numeric suffix is reserved for volumes, `a` refuses archive names that end in three or more digits, such as `data.2024`.

If a volume in the middle of a set is missing, `czip` names the missing file instead of reporting a corrupt archive.

## Patterns

Arguments after the archive name select entries for `x`, `e`, `l`, and `t`:

- A pattern without `/` matches a name at any depth: `'*.txt'`, `node_modules`.
- A pattern with `/` matches the full path: `'docs/*.md'`. `*` does not cross `/`.
- A pattern that matches a directory selects everything below it.

Matching is case-sensitive. Quote patterns so the shell does not expand them first.

## Exit codes

- `0`: success.
- `1`: finished, but something was skipped (an existing file without `-y`, an unsafe path, an unreadable input, no entry matching a pattern).
- `2`: error (bad arguments, missing archive or volume, CRC failure, write failure).

## Safety

- Archives are written to `*.part` files and renamed only after the whole archive is complete, so a failed run never leaves a half-written archive under the final name and never destroys the archive it was replacing.
- Files are extracted to a temporary file and moved into place only after the CRC matches. A corrupt entry is reported and not left behind.
- Entries with absolute paths or `..` components are refused, and symbolic-link entries are skipped, so an archive cannot write outside the output directory.
- If any input named on the `a` command line does not exist, nothing is created.

## Limitations

- No encryption. Encrypted entries are reported as errors.
- `a` always creates a new archive; it cannot add to or update an existing one.
- File names are written as UTF-8. Names stored in a legacy code page such as CP949 are not converted when reading.
- Symbolic links are stored as the file they point to. Links to directories are followed only when named on the command line.
- If the process is killed, `*.part` or `.czip-*.tmp` files can remain. They are safe to delete.
- Developed and tested on Linux; not tested on Windows.

## Tests

```sh
go test ./...
```

The interoperability tests run `7z` and `unzip` and are skipped when those tools are not installed.
