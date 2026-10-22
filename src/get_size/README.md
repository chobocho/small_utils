# getsize

A small Go utility that recursively totals regular-file sizes in one or more directories.

## Build

```sh
./build.sh
```

This creates the `getsize` executable in this directory. You can also build directly with `go build -o getsize .`.

## Usage

```text
getsize [options] [DIR...]
```

When run without options or DIR arguments, `getsize` defaults to the current directory and displays its name and total size, followed by a human-readable, size-sorted list of its immediate child directories and their sizes.

Options:

- `-d`: show the recursive size of every descendant directory (the default view shows only immediate child directories).
- `--min-size SIZE`: set a minimum size threshold for large-item reports. Supports bytes or 1024-based suffixes such as `100K`, `10M`, and `1.5G`.
- `--large-files`: list files whose individual size is at least `--min-size`.
- `--large-dirs`: list directories whose recursive total is at least `--min-size` (includes the input directory).
- `--large-all`: list qualifying files and directories together. Output has size, type (`file`/`dir`), and path columns. Use `-S` to sort largest first.
- `-h`: print sizes in human-readable units (1024-based, like `ls -h`).
- `-S`: sort displayed entries by size, largest first. Equal sizes are sorted by path. This is already the default for an unoptioned run.
- `--max-depth N`: limit the depth of displayed descendant directories. The root is depth 0; `0` shows no descendants. This affects display only, not the recursive total. The default view shows depth 1.
- `--exclude PATTERN`: exclude matching relative paths or basenames. Repeat the option to specify multiple patterns; excluded directories are skipped recursively.
- `--json`: output JSON with byte counts.
- `--help`: show help. (The short `-h` is reserved for human-readable sizes.)
- `--version`: print the version (`V0.1.0`).

Examples:

```sh
./getsize
./getsize -h
./getsize --version
./getsize ~/Downloads
./getsize -d -h -S --max-depth 2 folder1 folder2
./getsize --exclude '*.tmp' --exclude cache --json folder
./getsize --min-size 100M --large-files -h folder
./getsize --min-size 1G --large-all -S folder
```

Large-item options require `--min-size`; when used, they print the selected matching items instead of the regular summary. `--max-depth` also limits directory results in large-directory reports. Hidden files are included. Symbolic links are not followed or counted. Files that cannot be read are reported to stderr; scanning continues where possible, and the program exits with a non-zero status if any scan errors occur. Multiple hard links are counted once per path encountered.
