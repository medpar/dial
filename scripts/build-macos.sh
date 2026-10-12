#!/usr/bin/env bash
# Builds dist/Dial.app (menu bar app + dial CLI) and dist/dial-<os>-<arch>.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)}"
LDFLAGS="-s -w -X main.version=${VERSION}"
mkdir -p dist

echo "→ dial CLI ${VERSION}"
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o dist/dial-darwin-arm64 ./cmd/dial
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o dist/dial-darwin-amd64 ./cmd/dial
lipo -create -output dist/dial-darwin-universal dist/dial-darwin-arm64 dist/dial-darwin-amd64
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o dist/dial-linux-amd64 ./cmd/dial
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o dist/dial-linux-arm64 ./cmd/dial

echo "→ menu bar app"
(cd macos && swift build -c release --product Dial >/dev/null)
BIN="$(cd macos && swift build -c release --show-bin-path)/Dial"

APP=dist/Dial.app
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" "$APP/Contents/Helpers"
cp "$BIN" "$APP/Contents/MacOS/Dial"
strip -x "$APP/Contents/MacOS/Dial" 2>/dev/null || true
cp dist/dial-darwin-universal "$APP/Contents/Helpers/dial"
cp macos/Resources/Dial.icns "$APP/Contents/Resources/Dial.icns"
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Dial</string>
  <key>CFBundleDisplayName</key><string>Dial</string>
  <key>CFBundleIdentifier</key><string>io.github.medpar.dial</string>
  <key>CFBundleExecutable</key><string>Dial</string>
  <key>CFBundleIconFile</key><string>Dial</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${VERSION%%-*}</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>LSMinimumSystemVersion</key><string>14.0</string>
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSHumanReadableCopyright</key><string>MIT License</string>
</dict>
</plist>
PLIST
codesign --force --deep --sign - "$APP" >/dev/null 2>&1
(cd dist && rm -f Dial-macos.zip && ditto -c -k --keepParent Dial.app Dial-macos.zip)
echo "✓ dist/Dial.app ($(du -sh "$APP" | cut -f1)), dist/dial-linux-amd64"
