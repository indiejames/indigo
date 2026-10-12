#!/bin/sh
# Assemble release archives from built binaries.
#
#   VERSION=v1.2.3 scripts/release/package.sh <bindir> <outdir>
#
# <bindir> holds what build-client.sh and build-portable.sh produced:
#   indigo-<os>-<arch>             one per client platform built
#   indigo-server-linux-{amd64,arm64}
#   plugins/<name>/...
#
# <outdir> receives:
#   indigo-<version>-<os>-<arch>.tar.gz   per client platform
#   indigo-plugins-<version>.tar.gz       every plugin, every platform
#   checksums.txt                          sha256 of each archive
#
# Each client archive carries both container servers beside the binary, which
# is the first place container.LocateServerBinary looks — so an unpacked
# release can open a dev container with nothing else installed.
set -eu

bindir=${1:?usage: package.sh <bindir> <outdir>}
outdir=${2:?usage: package.sh <bindir> <outdir>}
version=${VERSION:?set VERSION, e.g. VERSION=v1.2.3}

# Keep macOS tar from adding ._ AppleDouble files when run locally.
export COPYFILE_DISABLE=1

mkdir -p "$outdir"
outdir=$(cd "$outdir" && pwd)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

for server in indigo-server-linux-amd64 indigo-server-linux-arm64; do
	[ -f "$bindir/$server" ] || { echo "missing $bindir/$server" >&2; exit 1; }
done

found=0
for client in "$bindir"/indigo-darwin-* "$bindir"/indigo-linux-*; do
	[ -f "$client" ] || continue
	platform=${client##*/indigo-}          # e.g. darwin-arm64
	name="indigo-$version-$platform"
	dir="$stage/$name"
	mkdir -p "$dir"
	cp "$client" "$dir/indigo"
	chmod 755 "$dir/indigo"   # CI artifacts lose the executable bit in transit
	cp "$bindir/indigo-server-linux-amd64" "$bindir/indigo-server-linux-arm64" "$dir/"
	chmod 755 "$dir"/indigo-server-linux-*
	cp LICENSE THIRD_PARTY_NOTICES.md README.md "$dir/"
	tar -C "$stage" -czf "$outdir/$name.tar.gz" "$name"
	echo "packaged $name.tar.gz"
	found=$((found + 1))
done
[ "$found" -gt 0 ] || { echo "no client binaries (indigo-<os>-<arch>) in $bindir" >&2; exit 1; }

if [ -d "$bindir/plugins" ]; then
	name="indigo-plugins-$version"
	mkdir -p "$stage/$name"
	cp -R "$bindir/plugins" "$stage/$name/"
	# CI artifacts lose the executable bit in transit; restore it.
	find "$stage/$name/plugins" -type f ! -name '*.toml' -exec chmod 755 {} +
	tar -C "$stage" -czf "$outdir/$name.tar.gz" "$name"
	echo "packaged $name.tar.gz"
fi

cd "$outdir"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum indigo-*.tar.gz > checksums.txt
else
	shasum -a 256 indigo-*.tar.gz > checksums.txt
fi
cat checksums.txt
