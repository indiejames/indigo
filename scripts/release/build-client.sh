#!/bin/sh
# Build the indigo client for one platform, with every grammar.
#
#   scripts/release/build-client.sh <goos> <goarch> <outfile>
#
# The client needs cgo (the tree-sitter grammars are C), so unlike the server
# and plugins it cannot be cross-built from one machine with CGO_ENABLED=0:
#
#   darwin  Built on a macOS runner. Both architectures come from one machine,
#           since Apple's clang targets either with -arch.
#   linux   Built inside Alpine, natively for each architecture, and linked
#           fully static against musl. A glibc build would only run on
#           distributions at least as new as the build machine; a static one
#           runs on any Linux, including the musl-based containers indigo
#           already supports.
#
# VERSION is stamped into `indigo --version`.
set -eu

goos=${1:?usage: build-client.sh <goos> <goarch> <outfile>}
goarch=${2:?usage: build-client.sh <goos> <goarch> <outfile>}
out=${3:?usage: build-client.sh <goos> <goarch> <outfile>}
version=${VERSION:-dev}

ldflags="-s -w -X main.version=$version"

case "$goos" in
darwin)
	case "$goarch" in
	amd64) clang_arch=x86_64 ;;
	arm64) clang_arch=arm64 ;;
	*) echo "unsupported darwin arch: $goarch" >&2; exit 1 ;;
	esac
	# The oldest macOS still receiving security updates when this was written.
	export MACOSX_DEPLOYMENT_TARGET=${MACOSX_DEPLOYMENT_TARGET:-13.0}
	export CC="clang -arch $clang_arch"
	export CXX="clang++ -arch $clang_arch"
	;;
linux)
	if ! ldd --version 2>&1 | grep -qi musl; then
		echo "linux builds must run on a musl system (Alpine) to link statically" >&2
		exit 1
	fi
	ldflags="$ldflags -linkmode external -extldflags -static"
	;;
*)
	echo "unsupported os: $goos" >&2
	exit 1
	;;
esac

echo "building indigo $version for $goos/$goarch"
CGO_ENABLED=1 GOOS=$goos GOARCH=$goarch \
	go build -trimpath -tags lang_all -ldflags="$ldflags" -o "$out" ./cmd/indigo

if [ "$goos" = linux ] && command -v file >/dev/null 2>&1; then
	file "$out"
	if ! file "$out" | grep -q 'statically linked\|static-pie linked'; then
		echo "expected a statically linked binary" >&2
		exit 1
	fi
fi
