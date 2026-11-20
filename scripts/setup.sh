#!/bin/sh
# Installs or verifies the pinned development tools on Linux and macOS. Stands alone: Go may be
# absent. Prints everything it will install, and asks, before installing anything.
#
#   scripts/setup.sh            Go 1.27.4 and git, then `go run ./tools/dev install-tools`
#                               (golangci-lint, GoReleaser, syft, actionlint, go-licenses, govulncheck)
#   scripts/setup.sh --spike    also a native ffmpeg with an HEVC encoder (libx265) and the
#                               wasmtime command-line runner, which only spike S1 needs
#   scripts/setup.sh --cross    also zig (the C cross-compiler of `tools/dev platforms`' race
#                               builds) and, on Linux, qemu-user-static (its linux/arm64 runs)
#   --yes                       do not ask (required when stdin is not a terminal)
#   --dry-run                   print the plan and stop
#
# Go, wasmtime and zig come from their vendors' release archives, checked against the SHA-256
# sums pinned below, and go under $QRVT_PREFIX (default ~/.local). git, curl, xz, ffmpeg and
# qemu-user-static come from the system package manager: Homebrew, apt, dnf or pacman.
# Each package-manager command is stopped after 15 minutes (apt-get update after 5), so that a
# stalled mirror fails with a message instead of hanging; QRVT_PM_TIMEOUT sets that limit in
# seconds for every such command (0: none).
set -eu
# Never let an older go on PATH download a toolchain before the plan is printed.
GOTOOLCHAIN=local
export GOTOOLCHAIN

GO_VERSION=1.27.4
WASMTIME_VERSION=50.0.1
ZIG_VERSION=0.17.1
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
# From https://ziglang.org/download/index.json.
zig_sha256() {
	case $1 in
	x86_64-linux) echo 3581863e3cbcfd0fce0efc682ce7e9e93da96668a977791d356a26d2a7e9b17a ;;
	aarch64-linux) echo a1546d5646eb184437918d6635ca6b762a0e889eac09cef720ee7a1ed3fe7795 ;;
	x86_64-macos) echo 9af07b20319fbd0c92ee46ad23e81d6a782b1aaea2cfb8dcbff35f1d2c7ddbb7 ;;
	aarch64-macos) echo 75f1bc7a1e9852f17ec9a0c1895f767c4ec56d46bc8a834920eece1d0359dd8d ;;
	*) return 1 ;;
	esac
}

spike=0 cross=0 yes=0 dry=0
for a in "$@"; do
	case $a in
	--spike) spike=1 ;;
	--cross) cross=1 ;;
	--yes | -y) yes=1 ;;
	--dry-run) dry=1 ;;
	-h | --help)
		sed -n '2,19p' "$0"
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

# Limits of the package-manager commands, in seconds: a stalled mirror otherwise hangs the setup
# without a word (PR #52's CI run waited 35 minutes in apt-get update). apt also gives up on a
# connection silent for 30 s and retries a failed download 3 times.
pm_timeout=${QRVT_PM_TIMEOUT:-900}
update_timeout=${QRVT_PM_TIMEOUT:-300}
case $pm_timeout in '' | *[!0-9]*) die "QRVT_PM_TIMEOUT must be a number of seconds, not '$pm_timeout'" ;; esac
apt_opts="-o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 -o Acquire::Retries=3"

# bounded SECONDS COMMAND...: runs COMMAND, limited to SECONDS (0: no limit). Past it the command
# gets SIGTERM, and 10 s later SIGKILL, with its child processes (where pkill exists); bounded
# then says so and returns 124. Otherwise it returns the command's status. Under sudo, SIGTERM
# reaches sudo, which relays it to the command, but SIGKILL cannot reach the command, which runs as
# root: a command that also ignores SIGTERM is then left to the CI step's own limit.
bounded() {
	# Every command here that can fail before the cleanup is guarded (|| ..., or set +e in the
	# watchdog and the trap), so that set -e cannot end the script half-way.
	b_limit=$1
	shift
	b_status=0
	if [ "$b_limit" -eq 0 ]; then
		"$@" || b_status=$?
		return "$b_status"
	fi
	b_mark=$(mktemp -d) || return 1
	"$@" &
	b_pid=$!
	# The watchdog, without set -e (a pkill that finds no child must not end it). Its sleeps run in
	# the background so that the TERM it gets when the command ends interrupts its wait at once; a
	# TERM that arrives before a sleep's process ID is known sets b_stop, checked right after.
	(
		set +e
		b_sleep='' b_stop=''
		trap 'if [ -n "$b_sleep" ]; then kill "$b_sleep" 2>/dev/null; exit 0; fi; b_stop=1' TERM
		sleep "$b_limit" &
		b_sleep=$!
		if [ -n "$b_stop" ]; then kill "$b_sleep" 2>/dev/null; exit 0; fi
		wait "$b_sleep" || exit 0
		: >"$b_mark/expired"
		kill -TERM "$b_pid" 2>/dev/null || exit 0
		b_sleep=''
		sleep 10 &
		b_sleep=$!
		if [ -n "$b_stop" ]; then kill "$b_sleep" 2>/dev/null; exit 0; fi
		wait "$b_sleep" || exit 0
		command -v pkill >/dev/null 2>&1 && pkill -KILL -P "$b_pid"
		kill -KILL "$b_pid"
	) </dev/null >/dev/null 2>&1 &
	b_dog=$!
	# Ctrl-C reaches the shell, not the command, which as a background job ignores SIGINT: stop
	# it, and the watchdog, rather than leave them running detached (holding dpkg's lock, say).
	# The trap is set only now: a watchdog forked with it in place could lose its TERM (under dash,
	# once in 60 runs, it then waited on its sleep for good). It runs without set -e, as any of its
	# kills may find nothing left to signal.
	trap 'set +e; command -v pkill >/dev/null 2>&1 && pkill -P "$b_dog"; kill -TERM "$b_pid" "$b_dog" 2>/dev/null; rm -rf "$b_mark"; exit 130' INT TERM
	wait "$b_pid" || b_status=$?
	trap - INT TERM
	# The watchdog's sleep first, by its parent, then the watchdog, whose trap also covers a sleep
	# not yet forked.
	if command -v pkill >/dev/null 2>&1; then pkill -P "$b_dog" 2>/dev/null || true; fi
	kill "$b_dog" 2>/dev/null || true
	wait "$b_dog" 2>/dev/null || true
	if [ -e "$b_mark/expired" ]; then
		printf 'setup: %s did not finish within %s s (a package mirror may be stalled); run setup again, or set QRVT_PM_TIMEOUT to a longer limit in seconds (0: none)\n' "$*" "$b_limit" >&2 || true
		b_status=124
	fi
	rm -rf "$b_mark" || true
	return "$b_status"
}

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

plan="" pkgs="" go_install=0 wasmtime_install=0 zig_install=0 qemu_install=""
add() { plan="$plan
  - $*"; }
need_pkg() {
	[ -n "$pm" ] || die "$1 is missing and no supported package manager (brew, apt, dnf, pacman) was found"
	pkgs="$pkgs $(pkgname "$1")"
	if [ "$pm" = pacman ]; then
		# Arch supports no partial upgrades: syncing the package database means upgrading the system.
		add "$1 from pacman (pacman -Syu, which also upgrades the installed packages)"
	else
		add "$1 from $pm"
	fi
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
# Cross-checking tools (tools/dev platforms).
if [ $cross = 1 ]; then
	if have zig && [ "$(zig version 2>/dev/null)" = "$ZIG_VERSION" ]; then
		say "ok  zig $ZIG_VERSION ($(command -v zig))"
	elif [ -x "$PREFIX/bin/zig" ] && [ "$("$PREFIX/bin/zig" version)" = "$ZIG_VERSION" ]; then
		say "ok  zig $ZIG_VERSION ($PREFIX/bin/zig, not on PATH)"
	else
		zig_install=1
		need_download=1
		add "zig $ZIG_VERSION from https://ziglang.org/download/$ZIG_VERSION/zig-$warch-$wos-$ZIG_VERSION.tar.xz (SHA-256 checked) into $PREFIX/opt, linked from $PREFIX/bin/zig"
		have xz || need_pkg xz
	fi
	# qemu-user-static, for linux/arm64 under emulation on an amd64 Linux host. Only the system
	# package manager installs it (not Homebrew), only with usable root rights, and never so that
	# a failure stops the rest of the setup.
	if [ $os = linux ] && [ $arch = amd64 ]; then
		qpm=""
		if have apt-get; then qpm=apt; elif have dnf; then qpm=dnf; elif have pacman; then qpm=pacman; fi
		if grep -qs '^enabled' /proc/sys/fs/binfmt_misc/qemu-aarch64; then
			say "ok  qemu for linux/arm64 (binfmt)"
		elif [ -z "$qpm" ]; then
			say "missing: qemu-user-static (no apt, dnf or pacman to install it with); tools/dev platforms will report linux/arm64 as not run"
		elif [ "$(id -u)" -eq 0 ] || { have sudo && sudo -n true 2>/dev/null; } || { have sudo && [ $yes = 0 ] && [ -t 0 ]; }; then
			qemu_install=$qpm
			add "qemu-user-static from $qpm (installed last; a failure there does not stop the setup)"
		else
			say "missing: qemu-user-static, which needs root (sudo is not usable without a password here); tools/dev platforms will report linux/arm64 as not run"
		fi
	fi
fi
if [ "$need_download" = 1 ] && ! have curl; then need_pkg curl; fi

# The Go tools are listed by tools/dev itself when Go is already in place.
if [ $go_install = 0 ]; then
	tools=$(PATH="${gobin:+$gobin:}$PATH" go run ./tools/dev install-tools -dry-run)
	case $tools in
	*"Will install"*) add "$(printf '%s' "$tools" | sed -n '2,$p' | sed 's/^ *//' | paste -sd ';' -)" ;;
	*) say "ok  pinned tools (golangci-lint, GoReleaser, syft, actionlint, go-licenses, govulncheck)" ;;
	esac
else
	add "then, if missing, the pinned tools of tools/dev: golangci-lint, GoReleaser, syft and actionlint (release archives, SHA-256 checked) and go-licenses and govulncheck (go install)"
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
	# A stalled transfer (under 1 KiB/s for a minute) fails and is retried, like any timeout.
	curl -fsSL --proto '=https' --tlsv1.2 --connect-timeout 30 --speed-limit 1024 --speed-time 60 --retry 3 -o "$3" "$1"
	got=$(sha256 "$3")
	[ "$got" = "$2" ] || die "checksum mismatch for $1: got $got, want $2"
}

if [ -n "$pkgs" ]; then
	# shellcheck disable=SC2086 # $sudo, $apt_opts and $pkgs are word lists
	case $pm in
	brew) bounded "$pm_timeout" brew install $pkgs ;;
	apt) bounded "$update_timeout" $sudo apt-get $apt_opts update && bounded "$pm_timeout" $sudo apt-get $apt_opts install -y $pkgs ;;
	dnf) bounded "$pm_timeout" $sudo dnf install -y $pkgs ;;
	pacman) bounded "$pm_timeout" $sudo pacman -Syu --needed --noconfirm $pkgs ;;
	esac || die "installing$pkgs with $pm failed"
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

if [ $zig_install = 1 ]; then
	name="zig-$warch-$wos-$ZIG_VERSION"
	sum=$(zig_sha256 "$warch-$wos") || die "no pinned zig archive for $warch-$wos"
	fetch "https://ziglang.org/download/$ZIG_VERSION/$name.tar.xz" "$sum" "$tmp/zig.tar.xz"
	mkdir -p "$PREFIX/opt" "$PREFIX/bin"
	tar -xJf "$tmp/zig.tar.xz" -C "$PREFIX/opt"
	ln -sf "$PREFIX/opt/$name/zig" "$PREFIX/bin/zig"
	say "installed zig $ZIG_VERSION into $PREFIX/opt/$name"
fi

if [ -n "$qemu_install" ]; then
	# shellcheck disable=SC2086 # $sudo and $apt_opts are word lists
	case $qemu_install in
	apt) bounded "$update_timeout" $sudo apt-get $apt_opts update && bounded "$pm_timeout" $sudo apt-get $apt_opts install -y qemu-user-static ;;
	dnf) bounded "$pm_timeout" $sudo dnf install -y qemu-user-static ;;
	pacman) bounded "$pm_timeout" $sudo pacman -Syu --needed --noconfirm qemu-user-static ;;
	esac || say "missing: qemu-user-static could not be installed; tools/dev platforms will report linux/arm64 as not run"
fi

if [ $spike = 1 ] && ! ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libx265; then
	die "the installed ffmpeg has no libx265 encoder"
fi

[ -n "$gobin" ] && PATH="$gobin:$PATH" && export PATH
go run ./tools/dev install-tools
go run ./tools/dev check-tools

if [ -n "$gobin" ] || [ $wasmtime_install = 1 ] || [ $zig_install = 1 ]; then
	say ""
	say "Add to your shell profile:"
	[ -n "$gobin" ] && say "  export PATH=\"$gobin:\$PATH\""
	{ [ $wasmtime_install = 1 ] || [ $zig_install = 1 ]; } && say "  export PATH=\"$PREFIX/bin:\$PATH\""
	say "  export PATH=\"\$(go env GOPATH)/bin:\$PATH\""
fi
say "setup complete"
