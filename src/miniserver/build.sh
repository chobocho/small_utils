#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$SCRIPT_DIR"

# Termux(PRoot 포함)의 Rust는 android 타깃이라 PATH의 리눅스 cc로는 링크가 실패한다.
# Termux clang이 있으면 링커로 지정한다.
TERMUX_CLANG=/data/data/com.termux/files/usr/bin/clang
if [ -x "$TERMUX_CLANG" ] && rustc -vV | grep -q 'host: .*-linux-android'; then
    export CARGO_TARGET_AARCH64_LINUX_ANDROID_LINKER="$TERMUX_CLANG"
fi

cargo build --release
cp target/release/miniserver "$SCRIPT_DIR/miniserver"
printf 'Built %s/miniserver\n' "$SCRIPT_DIR"
