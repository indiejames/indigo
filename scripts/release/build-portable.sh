#!/bin/sh
# Build the parts of a release that need no cgo, for every platform at once:
# the static Linux container servers and every bundled plugin.
#
#   scripts/release/build-portable.sh <outdir>
#
# Produces:
#   <outdir>/indigo-server-linux-{amd64,arm64}
#   <outdir>/plugins/<name>/plugin.toml
#   <outdir>/plugins/<name>/<name>-{darwin,linux}-{amd64,arm64}
#
# CGO_ENABLED=0 throughout, for the reason `make build-container-server` gives:
# static binaries run on any base image, glibc or musl. Go ad-hoc signs
# darwin/arm64 binaries itself, so cross-built macOS plugins run there too.
#
# PLUGIN_PLATFORMS narrows which plugin platforms are built and BUILD_SERVERS=0
# skips the servers. The release workflow uses both so the macOS plugins are
# built on the macOS runner, where they can be signed with the client.
set -eu

out=${1:?usage: build-portable.sh <outdir>}
version=${VERSION:-dev}
mkdir -p "$out/plugins"

platforms=${PLUGIN_PLATFORMS:-darwin/amd64 darwin/arm64 linux/amd64 linux/arm64}

if [ "${BUILD_SERVERS:-1}" = 1 ]; then
	for arch in amd64 arm64; do
		echo "building indigo-server for linux/$arch"
		CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
			go build -trimpath -ldflags="-s -w" -o "$out/indigo-server-linux-$arch" ./cmd/indigo-server
	done
fi

for manifest in plugins/*/plugin.toml; do
	dir=$(dirname "$manifest")
	name=$(basename "$dir")
	mkdir -p "$out/plugins/$name"
	cp "$manifest" "$out/plugins/$name/"
	for platform in $platforms; do
		os=${platform%/*}
		arch=${platform#*/}
		echo "building plugin $name for $os/$arch"
		CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
			go build -trimpath -ldflags="-s -w" -o "$out/plugins/$name/$name-$os-$arch" "./$dir"
	done
done

echo "portable build $version done: $out"
