# Small Utils

## Simple Awk

> awk '/pattern/' filename

> awk "'/ SUPER| STAR/'" dictionary.txt

## Correct File

Fix Korean filenames received from macOS (NFD) so they display correctly on Windows (NFC).

> correctfile "인터넷 활용 가이드*.pdf"

> correctfile -n *        (dry-run: show new names only)

> correctfile -r folder   (process subdirectories recursively)

## Get Size

Recursively total file sizes in directories (Go). Shows the folder's total and its child directories sorted by size.

> getsize                 (current folder)

> getsize -h --large-files --min-size 100M dir

## Mini Server

Tiny static web server (Rust). Serves the current folder, opening `index.html` by default. Type `quit` or run `miniserver --stop` to stop.

> miniserver              (port 8811, current folder)

> miniserver -p 9000 -d ~/mysite

> miniserver -s &          (silent: ignore `quit`, run in background)

> miniserver --stop        (stop the server on port 8811; add -p PORT for others)

## Czip

ZIP archiver with split volumes and ARJ-style commands (Go). Volumes are named `backup.zip.001`, `.002`, …; 7-Zip opens them directly and `cat backup.zip.* > backup.zip` rejoins them.

> czip a backup.zip docs photos            (create; directories are recursed)

> czip a -v100m -m9 backup.zip photos      (split into 100 MB volumes, best compression)

> czip l backup.zip                        (list; `t` tests CRC)

> czip x -orestore backup.zip              (extract with paths; `e` flattens, `-y` overwrites)
