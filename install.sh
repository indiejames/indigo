#!/bin/sh
# Install indigo from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/indiejames/indigo/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/indiejames/indigo/main/install.sh | sh -s -- --plugins
#
# Options:
#   --version <tag>   install a specific release (default: the latest)
#   --dir <path>      where to put the indigo binary (default: ~/.local/bin)
#   --plugins         also install the bundled plugins
#   --plugins-only    install only the bundled plugins (e.g. after `brew install`)
#
# Environment equivalents: INDIGO_VERSION, INDIGO_INSTALL_DIR.
#
# What goes where:
#   <dir>/indigo                                   the editor
#   ~/.indigo/indigo-server-linux-{amd64,arm64}    servers copied into dev containers
#   ~/.config/indigo/plugins/<name>/               plugins, with --plugins
#                                                  (or $XDG_CONFIG_HOME/indigo/plugins)
set -eu

# Everything runs inside main, called on the last line. Piped from curl, sh
# executes the script as it arrives, so a download cut off partway would
# otherwise run whatever had arrived; a truncated copy never reaches the call.
main() {

repo="indiejames/indigo"
# Overridable so the script can be tested against a local directory
# (INDIGO_RELEASE_BASE=file:///path/to/dist).
release_base=${INDIGO_RELEASE_BASE:-https://github.com/$repo/releases/download}

version=${INDIGO_VERSION:-}
bindir=${INDIGO_INSTALL_DIR:-$HOME/.local/bin}
want_client=1
want_plugins=0

say() { printf '%s\n' "$*"; }
die() { printf 'indigo install: %s\n' "$*" >&2; exit 1; }

# Inline rather than read from $0, which is just "sh" when piped from curl.
usage() {
	cat <<'EOF'
Install indigo from a GitHub release.

  install.sh [--version <tag>] [--dir <path>] [--plugins | --plugins-only]

  --version <tag>   install a specific release (default: the latest)
  --dir <path>      where to put the indigo binary (default: ~/.local/bin)
  --plugins         also install the bundled plugins
  --plugins-only    install only the bundled plugins (e.g. after `brew install`)
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version) [ $# -ge 2 ] || die "--version needs a value"; version=$2; shift 2 ;;
	--version=*) version=${1#*=}; shift ;;
	--dir) [ $# -ge 2 ] || die "--dir needs a value"; bindir=$2; shift 2 ;;
	--dir=*) bindir=${1#*=}; shift ;;
	--plugins) want_plugins=1; shift ;;
	--plugins-only) want_plugins=1; want_client=0; shift ;;
	-h|--help) usage; exit 0 ;;
	*) die "unknown option: $1 (try --help)" ;;
	esac
done

# --- platform ---------------------------------------------------------------

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) die "unsupported OS $(uname -s); indigo supports macOS and Linux" ;;
esac
case "$(uname -m)" in
x86_64|amd64) arch=amd64 ;;
arm64|aarch64) arch=arm64 ;;
*) die "unsupported architecture $(uname -m); indigo supports amd64 and arm64" ;;
esac
# Under Rosetta, uname reports x86_64 on Apple silicon; prefer the native build.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
	[ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
	arch=arm64
fi

# --- download helpers -------------------------------------------------------

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
	latest_url() { curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q -O "$2" "$1"; }
	latest_url() { wget -q -S --spider "https://github.com/$repo/releases/latest" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r'; }
else
	die "need curl or wget"
fi

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
	die "need sha256sum or shasum to verify the download"
fi

if [ -z "$version" ]; then
	# The /releases/latest page redirects to /releases/tag/<tag>. Reading the
	# redirect avoids the GitHub API and its unauthenticated rate limit.
	version=$(latest_url) || die "could not determine the latest release"
	version=${version##*/}
	# Calendar-version tags (2026.10.0); v* for releases before the switch.
	case "$version" in
	[0-9][0-9][0-9][0-9].*|v*) ;;
	*) die "could not determine the latest release (got '$version')" ;;
	esac
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "indigo $version for $os/$arch"
fetch "$release_base/$version/checksums.txt" "$tmp/checksums.txt" ||
	die "could not download checksums for $version — does that release exist?"

# download <archive name>: fetch it into $tmp and verify it against checksums.txt.
download() {
	fetch "$release_base/$version/$1" "$tmp/$1" || die "could not download $1"
	want=$(awk -v f="$1" '$2 == f { print $1 }' "$tmp/checksums.txt")
	[ -n "$want" ] || die "$1 is not listed in checksums.txt"
	got=$(sha256 "$tmp/$1")
	[ "$want" = "$got" ] || die "checksum mismatch for $1 (expected $want, got $got)"
	tar -C "$tmp" -xzf "$tmp/$1"
}

# install_file <src> <dest>: replace dest atomically, so a running indigo
# keeps its old binary rather than seeing a half-written one.
install_file() {
	cp "$1" "$2.tmp.$$"
	chmod 755 "$2.tmp.$$"
	mv -f "$2.tmp.$$" "$2"
}

# warm <binary>: macOS checks a binary's signature the first time it runs,
# which can take seconds; do it now rather than on first launch (see --warm).
warm() {
	[ "$os" = darwin ] || return 0
	xattr -d com.apple.quarantine "$1" 2>/dev/null || true
	"$1" --warm >/dev/null 2>&1 || true
}

# --- the editor -------------------------------------------------------------

if [ "$want_client" = 1 ]; then
	name="indigo-$version-$os-$arch"
	download "$name.tar.gz"

	mkdir -p "$bindir" 2>/dev/null || die "cannot create $bindir (try --dir, or run with sudo)"
	[ -w "$bindir" ] || die "$bindir is not writable (try --dir, or run with sudo)"
	install_file "$tmp/$name/indigo" "$bindir/indigo"
	warm "$bindir/indigo"
	say "installed $bindir/indigo"

	servers="$HOME/.indigo"
	mkdir -p "$servers"
	for s in indigo-server-linux-amd64 indigo-server-linux-arm64; do
		install_file "$tmp/$name/$s" "$servers/$s"
	done
	say "installed dev-container servers in $servers"
fi

# --- plugins ----------------------------------------------------------------

if [ "$want_plugins" = 1 ]; then
	name="indigo-plugins-$version"
	download "$name.tar.gz"

	plugdir="${XDG_CONFIG_HOME:-$HOME/.config}/indigo/plugins"
	for src in "$tmp/$name/plugins"/*/; do
		p=$(basename "$src")
		mkdir -p "$plugdir/$p"
		# Every platform's binary, not just this one: a dev container's server
		# runs the Linux build of each plugin from this same directory.
		for f in "$src"*; do
			case "$f" in
			*.toml) cp "$f" "$plugdir/$p/" ;;
			*) install_file "$f" "$plugdir/$p/$(basename "$f")" ;;
			esac
		done
		[ -x "$plugdir/$p/$p-$os-$arch" ] && warm "$plugdir/$p/$p-$os-$arch"
		say "installed plugin $p"
	done
	say "plugins are in $plugdir"
fi

# --- afterwards -------------------------------------------------------------

if [ "$want_client" = 1 ]; then
	case ":$PATH:" in
	*":$bindir:"*) ;;
	*)
		say ""
		say "$bindir is not on your PATH. Add it, e.g.:"
		case "${SHELL:-}" in
		*/fish) say "  fish_add_path $bindir" ;;
		*/zsh) say "  echo 'export PATH=\"$bindir:\$PATH\"' >> ~/.zshrc" ;;
		*) say "  echo 'export PATH=\"$bindir:\$PATH\"' >> ~/.bashrc" ;;
		esac
		;;
	esac
	say ""
	say "If indigo was already running, close its windows to pick up the new version."
fi

}

main "$@"
