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

Tiny static web server (Rust). Serves the current folder, opening `index.html` by default. Type `quit` to stop.

> miniserver              (port 8811, current folder)

> miniserver -p 9000 -d ~/mysite
