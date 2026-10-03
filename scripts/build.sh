#!/usr/bin/env bash
# AirType 统一构建入口（Git Bash / Linux 均可）。用法：
#   scripts/build.sh all|test|bin|icons|uishot|winres|clean
set -euo pipefail
cd "$(dirname "$0")/.."

GO=${GO:-go}
if [ -x "/c/Program Files/Go/bin/go" ]; then GO="/c/Program Files/Go/bin/go"; fi

cmd_all() {
	cmd_test
	cmd_bin
}

cmd_test() {
	"$GO" vet ./...
	"$GO" test ./... -count=1
	echo "vet+test 全绿"
}

cmd_bin() {
	mkdir -p build
	"$GO" build -trimpath -ldflags "-s -w" -o build/traytest.exe ./cmd/traytest
	"$GO" build -trimpath -ldflags "-s -w" -o build/selftest.exe ./cmd/selftest
	"$GO" build -trimpath -ldflags "-s -w" -o build/qqtest.exe ./cmd/qqtest
	"$GO" build -trimpath -ldflags "-s -w" -o build/typetest-target.exe ./cmd/typetest-target
	CGO_ENABLED=0 "$GO" build -trimpath -ldflags "-H windowsgui -s -w" -o build/airtype.exe ./cmd/airtype
	echo "产物输出到 build/"
}

cmd_icons() {
	"$GO" run ./cmd/icongen
	echo "图标已生成到 internal/ui/icons/"
}

cmd_uishot() {
	"$GO" build -o "$TEMP/uishot.exe" ./cmd/uishot
	local out="$LOCALAPPDATA/Temp/uishot-latest"
	rm -rf "$out" && mkdir -p "$out"
	UISHOT_OUT="$(cygpath -w "$out")" "$TEMP/uishot.exe"
	echo "截图输出到 $out"
}

cmd_winres() {
	"$GO" run github.com/tc-hib/go-winres@v0.1.1 make --arch amd64 --in winres/winres.json --out cmd/airtype/rsrc
	echo "开发版 .syso 已重新生成（Release 版本号由 CI 按 tag 注入）"
}

cmd_clean() {
	rm -rf build
}

case "${1:-all}" in
all) cmd_all ;;
test) cmd_test ;;
bin) cmd_bin ;;
icons) cmd_icons ;;
uishot) cmd_uishot ;;
winres) cmd_winres ;;
clean) cmd_clean ;;
*)
	echo "用法: scripts/build.sh all|test|bin|icons|uishot|winres|clean" >&2
	exit 1
	;;
esac
