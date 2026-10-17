# Small Utils

## Simple Awk

> awk '/pattern/' filename

> awk "'/ SUPER| STAR/'" dictionary.txt

## Correct File

Fix Korean filenames received from macOS (NFD) so they display correctly on Windows (NFC).

> correctfile "인터넷 활용 가이드*.pdf"

> correctfile -n *        (dry-run: show new names only)

> correctfile -r folder   (process subdirectories recursively)
