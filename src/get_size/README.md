# get_size

A small Go utility that recursively totals regular-file sizes in one or more directories.

## Build

```sh
./build.sh
```

This creates the `get_size` executable in this directory. You can also build directly with `go build -o get_size .`.

## Usage

```text
get_size [options] DIR...
```

Options:

- `-d`: also show the recursive size of every descendant directory.
- `--min-size SIZE`: set a minimum size threshold for large-item reports. Supports bytes or 1024-based suffixes such as `100K`, `10M`, and `1.5G`.
- `--large-files`: list files whose individual size is at least `--min-size`.
- `--large-dirs`: list directories whose recursive total is at least `--min-size` (includes the input directory).
- `--large-all`: list qualifying files and directories together. Output has size, type (`file`/`dir`), and path columns. Use `-S` to sort largest first.
- `-h`: print sizes in human-readable units (1024-based, like `ls -h`).
- `-S`: sort displayed entries by size, largest first. Equal sizes are sorted by path.
- `--max-depth N`: limit the depth of descendant directories shown with `-d`. The root is depth 0; `0` shows no descendants. This affects display only, not the recursive total.
- `--exclude PATTERN`: exclude matching relative paths or basenames. Repeat the option to specify multiple patterns; excluded directories are skipped recursively.
- `--json`: output JSON with byte counts.
- `--help`: show help. (The short `-h` is reserved for human-readable sizes.)

Examples:

```sh
./get_size ~/Downloads
./get_size -d -h -S --max-depth 2 folder1 folder2
./get_size --exclude '*.tmp' --exclude cache --json folder
./get_size --min-size 100M --large-files -h folder
./get_size --min-size 1G --large-all -S folder
```

Large-item options require `--min-size`; when used, they print the selected matching items instead of the regular summary. `--max-depth` also limits directory results in large-directory reports. Hidden files are included. Symbolic links are not followed or counted. Files that cannot be read are reported to stderr; scanning continues where possible, and the program exits with a non-zero status if any scan errors occur. Multiple hard links are counted once per path encountered.
