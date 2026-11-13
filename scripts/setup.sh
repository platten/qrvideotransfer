#!/bin/sh
# Installs or verifies the pinned development tools on Linux and macOS. Stands alone: Go may be
# absent. Prints everything it will install, and asks, before installing anything.
#
#   scripts/setup.sh            Go 1.27.4 and git, then `go run ./tools/dev install-tools`
#                               (golangci-lint v2.15.0, GoReleaser v2.20.2)
#   scripts/setup.sh --spike    also a native ffmpeg with an HEVC encoder (libx265) and the
#                               wasmtime command-line runner, which only spike S1 needs
#   --yes                       do not ask (required when stdin is not a terminal)
#   --dry-run                   print the plan and stop
#
# Go and wasmtime come from their vendors' release archives, checked against the SHA-256 sums
# pinned below, and go under $QRVT_PREFIX (default ~/.local). git, curl, xz and ffmpeg come from
# the system package manager: Homebrew, apt, dnf or pacman.
set -eu
# Never let an older go on PATH download a toolchain before the plan is printed.
GOTOOLCHAIN=local
export GOTOOLCHAIN

GO_VERSION=1.27.4
WASMTIME_VERSION=50.0.1
PREFIX=${QRVT_PREFIX:-$HOME/.local}

# SHA-256 of the release archives, from https://go.dev/dl/?mode=json and the GitHub release assets
# of bytecodealliance/wasmtime.
go_sha256() {
	case $1 in
	linux-amd64) echo 7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840 ;;
	linux-arm64) echo 3392582988a273a87ab5043ee1b2c9c17a2c3c00580bf9326c47a2e0aaaeac92 ;;
	darwin-amd64) echo 2b799377704ef12db39bd254995d5c348c9cc8d86562ca817724390e56c38f43 ;;
	darwin-arm64) echo 91a44a2b8a716600ad94a4f143e156116620db8828086a6e3602291be9bd0bb4 ;;
	*) return 1 ;;
	esac
}
wasmtime_sha256() {
	case $1 in
	x86_64-linux) echo 685466455dd42a14774a86c36c8f2e45cbb9bd6d92a850139315df60a75498a5 ;;
	aarch64-linux) echo d737e077416d78fbfb0a008229ec27d6a0fd5a4924874b8c38b293c850cf4094 ;;
	x86_64-macos) echo ea1818fa8cbb51524847ab2ad22197e0051594fdc96cbd0cc2afb65832e11925 ;;
	aarch64-macos) echo 960e5216c7ebd7e9b45e6561d15821b8b801b7d54aee28c7b91921aeb30c65c8 ;;
	*) return 1 ;;
	esac
}

spike=0 yes=0 dry=0
for a in "$@"; do
	case $a in
	--spike) spike=1 ;;
	--yes | -y) yes=1 ;;
	--dry-run) dry=1 ;;
	-h | --help)
		sed -n '2,15p' "$0"
		exit 0
		;;
	*)
		echo "setup: unknown option $a" >&2
		exit 2
		;;
	esac
done

cd "$(dirname "$0")/.."
say() { printf '%s\n' "$*"; }
die() {
	printf 'setup: %s\n' "$*" >&2
	exit 1
}
have() { command -v "$1" >/dev/null 2>&1; }

case $(uname -s) in
Linux) os=linux wos=linux ;;
Darwin) os=darwin wos=macos ;;
*) die "unsupported system $(uname -s); on Windows use scripts/setup.ps1" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 warch=x86_64 ;;
aarch64 | arm64) arch=arm64 warch=aarch64 ;;
*) die "unsupported architecture $(uname -m)" ;;
esac

pm=""
if have brew; then
	pm=brew
elif have apt-get; then
	pm=apt
elif have dnf; then
	pm=dnf
elif have pacman; then
	pm=pacman
fi
sudo=""
if [ "$(id -u)" -ne 0 ] && have sudo; then sudo=sudo; fi

# Package names differ only for xz.
pkgname() {
	if [ "$1" = xz ] && [ "$pm" = apt ]; then echo xz-utils; else echo "$1"; fi
}

plan="" pkgs="" go_install=0 wasmtime_install=0
add() { plan="$plan
  - $*"; }
need_pkg() {
	[ -n "$pm" ] || die "$1 is missing and no supported package manager (brew, apt, dnf, pacman) was found"
	pkgs="$pkgs $(pkgname "$1")"
	add "$1 from $pm"
}

# Go.
GOROOT_NEW="$PREFIX/go$GO_VERSION"
gobin=""
if have go && [ "$(go env GOVERSION 2>/dev/null)" = "go$GO_VERSION" ]; then
	say "ok  Go $GO_VERSION ($(command -v go))"
elif [ -x "$GOROOT_NEW/bin/go" ] && [ "$("$GOROOT_NEW/bin/go" env GOVERSION)" = "go$GO_VERSION" ]; then
	gobin="$GOROOT_NEW/bin"
	say "ok  Go $GO_VERSION ($gobin/go, not on PATH)"
else
	go_install=1
	gobin="$GOROOT_NEW/bin"
	add "Go $GO_VERSION from https://go.dev/dl/go$GO_VERSION.$os-$arch.tar.gz (SHA-256 checked) into $GOROOT_NEW"
fi

# git, and curl for downloads.
if have git; then say "ok  git"; else need_pkg git; fi
need_download=$go_install

# Spike tools.
if [ $spike = 1 ]; then
	if have ffmpeg && ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libx265; then
		say "ok  ffmpeg with libx265 ($(command -v ffmpeg))"
	else
		[ "$pm" = dnf ] && say "note: on Fedora the ffmpeg package with libx265 needs RPM Fusion (https://rpmfusion.org) enabled"
		need_pkg ffmpeg
	fi
	if have wasmtime && wasmtime --version 2>/dev/null | grep -q " $WASMTIME_VERSION"; then
		say "ok  wasmtime $WASMTIME_VERSION ($(command -v wasmtime))"
	elif [ -x "$PREFIX/bin/wasmtime" ] && "$PREFIX/bin/wasmtime" --version | grep -q " $WASMTIME_VERSION"; then
		say "ok  wasmtime $WASMTIME_VERSION ($PREFIX/bin/wasmtime, not on PATH)"
	else
		wasmtime_install=1
		need_download=1
		add "wasmtime $WASMTIME_VERSION from the GitHub release wasmtime-v$WASMTIME_VERSION-$warch-$wos.tar.xz (SHA-256 checked) into $PREFIX/opt, linked from $PREFIX/bin/wasmtime"
		have xz || need_pkg xz
	fi
fi
if [ "$need_download" = 1 ] && ! have curl; then need_pkg curl; fi

# The Go tools are listed by tools/dev itself when Go is already in place.
if [ $go_install = 0 ]; then
	tools=$(PATH="${gobin:+$gobin:}$PATH" go run ./tools/dev install-tools -dry-run)
	case $tools in
	*"Will install"*) add "$(printf '%s' "$tools" | sed -n '2,$p' | sed 's/^ *//' | paste -sd ';' -)" ;;
	*) say "ok  Go tools (golangci-lint, GoReleaser)" ;;
	esac
else
	add "then, if missing: golangci-lint v2.15.0 and GoReleaser v2.20.2 (go install, through tools/dev)"
fi

if [ -z "$plan" ]; then
	say "Everything is installed."
	exit 0
fi
say "setup will install:$plan"
[ $dry = 1 ] && exit 0
if [ $yes = 0 ]; then
	[ -t 0 ] || die "stdin is not a terminal; re-run with --yes to install"
	printf 'Proceed? [y/N] '
	read -r answer
	case $answer in y | Y | yes) ;; *) die "nothing installed" ;; esac
fi

sha256() {
	if have sha256sum; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fetch() { # url sha256 file
	say "downloading $1"
	curl -fsSL --proto '=https' --tlsv1.2 -o "$3" "$1"
	got=$(sha256 "$3")
	[ "$got" = "$2" ] || die "checksum mismatch for $1: got $got, want $2"
}

if [ -n "$pkgs" ]; then
	# shellcheck disable=SC2086 # $pkgs is a word list
	case $pm in
	brew) brew install $pkgs ;;
	apt) $sudo apt-get update && $sudo apt-get install -y $pkgs ;;
	dnf) $sudo dnf install -y $pkgs ;;
	pacman) $sudo pacman -S --needed --noconfirm $pkgs ;;
	esac
fi

if [ $go_install = 1 ]; then
	sum=$(go_sha256 "$os-$arch") || die "no pinned Go archive for $os-$arch"
	fetch "https://go.dev/dl/go$GO_VERSION.$os-$arch.tar.gz" "$sum" "$tmp/go.tar.gz"
	mkdir -p "$tmp/go-extract" "$PREFIX"
	tar -xzf "$tmp/go.tar.gz" -C "$tmp/go-extract"
	mv "$tmp/go-extract/go" "$GOROOT_NEW"
	say "installed Go $GO_VERSION into $GOROOT_NEW"
fi

if [ $wasmtime_install = 1 ]; then
	name="wasmtime-v$WASMTIME_VERSION-$warch-$wos"
	sum=$(wasmtime_sha256 "$warch-$wos") || die "no pinned wasmtime archive for $warch-$wos"
	fetch "https://github.com/bytecodealliance/wasmtime/releases/download/v$WASMTIME_VERSION/$name.tar.xz" "$sum" "$tmp/wasmtime.tar.xz"
	mkdir -p "$PREFIX/opt" "$PREFIX/bin"
	tar -xJf "$tmp/wasmtime.tar.xz" -C "$PREFIX/opt"
	ln -sf "$PREFIX/opt/$name/wasmtime" "$PREFIX/bin/wasmtime"
	say "installed wasmtime $WASMTIME_VERSION into $PREFIX/opt/$name"
fi

if [ $spike = 1 ] && ! ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libx265; then
	die "the installed ffmpeg has no libx265 encoder"
fi

[ -n "$gobin" ] && PATH="$gobin:$PATH" && export PATH
go run ./tools/dev install-tools
go run ./tools/dev check-tools

if [ -n "$gobin" ] || [ $wasmtime_install = 1 ]; then
	say ""
	say "Add to your shell profile:"
	[ -n "$gobin" ] && say "  export PATH=\"$gobin:\$PATH\""
	[ $wasmtime_install = 1 ] && say "  export PATH=\"$PREFIX/bin:\$PATH\""
	say "  export PATH=\"\$(go env GOPATH)/bin:\$PATH\""
fi
say "setup complete"
