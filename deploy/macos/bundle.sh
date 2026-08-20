#!/bin/bash
# Assemble springback.app. Run on a Mac; needs Xcode's command line tools and a container runtime.
#
# The daemon is still cross-compiled in the pinned Linux toolchain container, so the Go half keeps
# the guarantee the Makefile exists to give. Everything below that line — Swift, dylib relocation,
# codesign, iconutil — has no container equivalent and runs on the host.
#
# THE libimobiledevice CLIs COME FROM HOMEBREW, and that is the one loose end. They are copied in
# and their install names rewritten, so the finished bundle depends on nothing outside itself; but
# reproducing the bundle needs `brew install libimobiledevice ideviceinstaller` first, at whatever
# version brew currently holds. Only ipatool is version-pinned here.
set -euo pipefail
cd "$(dirname "$0")/../.."
. ./versions.env

RUNTIME=${RUNTIME:-$(command -v container || command -v nerdctl || command -v docker)}
OUT=${OUT:-build}
APP="$OUT/springback.app"
IPATOOL_TARBALL="ipatool-${IPATOOL_REF#v}-macos-arm64.tar.gz"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Frameworks" "$APP/Contents/Resources" "$OUT/dl"

echo "==> daemon (darwin/arm64, built in $GO_IMAGE)"
"$RUNTIME" run --rm -v "$PWD:/src" -w /src/core "$GO_IMAGE" \
	env GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o "/src/$APP/Contents/MacOS/springback-server" ./cmd/springback

echo "==> ipatool $IPATOOL_REF (upstream release, checksum pinned in versions.env)"
if [ ! -f "$OUT/dl/$IPATOOL_TARBALL" ]; then
	curl -fsSL -o "$OUT/dl/$IPATOOL_TARBALL" \
		"https://github.com/majd/ipatool/releases/download/$IPATOOL_REF/$IPATOOL_TARBALL"
fi
echo "$IPATOOL_MACOS_ARM64_SHA256  $OUT/dl/$IPATOOL_TARBALL" | shasum -a 256 -c -
tar -xzf "$OUT/dl/$IPATOOL_TARBALL" -C "$OUT/dl"
cp "$OUT/dl/bin/ipatool-${IPATOOL_REF#v}-macos-arm64" "$APP/Contents/MacOS/ipatool"

echo "==> libimobiledevice CLIs and their dylibs"
deps() { otool -L "$1" | tail -n +2 | awk '{print $1}' | grep -vE '^/usr/lib/|^/System/' || true; }
for tool in idevice_id ideviceinfo idevicepair ideviceinstaller; do
	cp -f "$(command -v "$tool")" "$APP/Contents/MacOS/$tool"
done
chmod +x "$APP/Contents/MacOS/"*
# Breadth-first: a dylib pulls in its own, so naming only the CLIs' direct dependencies leaves
# dangling references. libzip reaches zstd and lzma this way.
queue=("$APP/Contents/MacOS/idevice_id" "$APP/Contents/MacOS/ideviceinfo" \
	"$APP/Contents/MacOS/idevicepair" "$APP/Contents/MacOS/ideviceinstaller")
seen=""
while [ ${#queue[@]} -gt 0 ]; do
	cur="${queue[0]}"; queue=("${queue[@]:1}")
	while read -r dep; do
		[ -n "$dep" ] || continue
		base=$(basename "$dep")
		if ! echo "$seen" | grep -qx "$base"; then
			seen="$seen
$base"
			cp -f "$dep" "$APP/Contents/Frameworks/$base"
			chmod u+w "$APP/Contents/Frameworks/$base"
			install_name_tool -id "@executable_path/../Frameworks/$base" "$APP/Contents/Frameworks/$base"
			queue+=("$APP/Contents/Frameworks/$base")
		fi
		install_name_tool -change "$dep" "@executable_path/../Frameworks/$base" "$cur"
	done < <(deps "$cur")
done

echo "==> shell and icon"
swiftc -O -target arm64-apple-macos13.0 -o "$APP/Contents/MacOS/springback" \
	deploy/macos/shell/main.swift -framework Cocoa -framework WebKit
swiftc -O -target arm64-apple-macos13.0 -o "$OUT/mkicon" deploy/macos/icon/mkicon.swift -framework Cocoa
"$OUT/mkicon" "$OUT/springback.iconset"
iconutil -c icns "$OUT/springback.iconset" -o "$APP/Contents/Resources/springback.icns"
cp deploy/macos/Info.plist "$APP/Contents/Info.plist"

# AD-HOC, AND EVERY MACH-O INDIVIDUALLY. install_name_tool invalidates a signature, and Apple
# silicon SIGKILLs an unsigned binary — the symptom is a tool that runs and prints nothing at all.
# Inner code first, then the bundle, or the bundle's seal covers files that change afterwards.
echo "==> codesign (ad-hoc; NOT notarized, so this will not travel to another Mac)"
for f in "$APP/Contents/Frameworks/"* "$APP/Contents/MacOS/"*; do
	case "$(file -b "$f")" in *Mach-O*) codesign --force --sign - "$f" >/dev/null;; esac
done
codesign --force --sign - "$APP" >/dev/null
codesign --verify --strict "$APP"

echo "==> $APP"
