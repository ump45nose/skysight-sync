#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
version=$(sed -n 's/^const Version = "\([^"]*\)"$/\1/p' internal/syncer/model.go)
[ -n "$version" ] || { echo 'Missing version' >&2; exit 1; }
name="skysight-sync_${version}_darwin_arm64"
mkdir -p "dist/$name/assets"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -buildvcs=false \
  -ldflags='-s -w' -o "dist/$name/skysight-sync" ./cmd/skysight-sync
cp README.md LICENSE THIRD_PARTY_NOTICES.txt "dist/$name/"
cp assets/icon.png assets/SkysightSync.icns assets/README.md "dist/$name/assets/"
COPYFILE_DISABLE=1 tar -czf "dist/$name.tar.gz" -C dist "$name"
cp assets/icon.png dist/SkysightSync.png
cp assets/SkysightSync.icns dist/SkysightSync.icns
(cd dist && shasum -a 256 "$name.tar.gz" SkysightSync.png SkysightSync.icns > SHA256SUMS)
printf '%s\n' "dist/$name.tar.gz" dist/SkysightSync.png dist/SkysightSync.icns dist/SHA256SUMS
