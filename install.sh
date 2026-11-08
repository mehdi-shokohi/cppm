#!/bin/sh
# cppm installer for Linux and macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/mehdi-shokohi/cppm/main/install.sh | sh
#
# Downloads the release binary for this platform, verifies its checksum, and
# installs it. Builds from source instead when --from-source is given or no
# release asset matches.
#
# Deliberately POSIX sh, not bash: macOS ships bash 3.2, and this needs nothing
# newer than sh anyway.

set -eu

REPO="${CPPM_REPO:-mehdi-shokohi/cppm}"
VERSION="${CPPM_VERSION:-latest}"
FROM_SOURCE=0

# Prefer a directory already on PATH that does not need root.
if [ -n "${CPPM_INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$CPPM_INSTALL_DIR"
elif [ -w "/usr/local/bin" ] 2>/dev/null; then
    INSTALL_DIR="/usr/local/bin"
else
    INSTALL_DIR="$HOME/.local/bin"
fi

say()  { printf '%s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'USAGE'
usage: install.sh [options]

  --version <v>      release to install (default: latest)
  --dir <path>       install directory (default: /usr/local/bin or ~/.local/bin)
  --from-source      build with the Go toolchain instead of downloading
  -h, --help         show this help

environment:
  CPPM_REPO, CPPM_VERSION, CPPM_INSTALL_DIR  equivalents of the above
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --version) VERSION="${2:?--version needs a value}"; shift 2 ;;
        --dir)     INSTALL_DIR="${2:?--dir needs a value}"; shift 2 ;;
        --from-source) FROM_SOURCE=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown option: $1 (try --help)" ;;
    esac
done

detect_platform() {
    os="$(uname -s)"
    arch="$(uname -m)"
    case "$os" in
        Linux)  os=linux ;;
        Darwin) os=darwin ;;
        *) die "unsupported operating system: $os (Windows users: use install.ps1)" ;;
    esac
    case "$arch" in
        x86_64|amd64)  arch=amd64 ;;
        arm64|aarch64) arch=arm64 ;;
        *) die "unsupported architecture: $arch" ;;
    esac
    printf '%s_%s' "$os" "$arch"
}

fetch() {
    # $1 url, $2 output path
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1" -o "$2"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "$2" "$1"
    else
        die "neither curl nor wget is available"
    fi
}

verify_checksum() {
    # $1 file, $2 checksums file, $3 asset name. A download nobody verified is a
    # download nobody should trust.
    expected="$(grep " $3\$" "$2" 2>/dev/null | awk '{print $1}' || true)"
    if [ -z "$expected" ]; then
        warn "no checksum listed for $3; skipping verification"
        return 0
    fi
    if command -v sha256sum >/dev/null 2>&1; then
        actual="$(sha256sum "$1" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then
        actual="$(shasum -a 256 "$1" | awk '{print $1}')"
    else
        warn "no sha256 tool available; skipping verification"
        return 0
    fi
    [ "$actual" = "$expected" ] || die "checksum mismatch for $3
  expected $expected
  actual   $actual"
    say "  checksum verified"
}

build_from_source() {
    command -v go >/dev/null 2>&1 || die "building from source needs the Go toolchain (https://go.dev/dl)"
    say "building cppm from source with $(go version)"
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    if [ -f "./go.mod" ] && [ -d "./cmd/cppm" ]; then
        src="."
    else
        say "  cloning https://github.com/$REPO"
        command -v git >/dev/null 2>&1 || die "git is required to clone the source"
        # --branch <tag> or the clone ignores the requested version and builds
        # whatever the default branch happens to be, which is not the release
        # the user asked for.
        if [ "$VERSION" = "latest" ]; then
            git clone --quiet --depth 1 "https://github.com/$REPO" "$tmp/src"
        else
            git clone --quiet --depth 1 --branch "$VERSION" "https://github.com/$REPO" "$tmp/src" \
                || die "no such version: $VERSION"
        fi
        src="$tmp/src"
    fi
    # Stamp the version when one was named. Go records the module version from
    # VCS tags by itself, so a local checkout on a tag is labelled correctly
    # without help -- but a --depth 1 clone carries no tags for it to read, and
    # an unstamped build then falls back to the in-source "-dev" string and
    # calls a real release a development build.
    ldflags="-s -w"
    if [ "$VERSION" != "latest" ] && [ "$src" != "." ]; then
        ldflags="$ldflags -X main.version=$VERSION"
    fi
    ( cd "$src" && go build -trimpath -ldflags "$ldflags" -o "$tmp/cppm" ./cmd/cppm )
    install_binary "$tmp/cppm"
}

install_binary() {
    # $1 path to the built or downloaded binary
    mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
    target="$INSTALL_DIR/cppm"
    if [ -w "$INSTALL_DIR" ]; then
        mv -f "$1" "$target"
    elif command -v sudo >/dev/null 2>&1; then
        say "  $INSTALL_DIR needs elevated permissions"
        sudo mv -f "$1" "$target"
    else
        die "$INSTALL_DIR is not writable and sudo is unavailable; try --dir \$HOME/.local/bin"
    fi
    chmod +x "$target" 2>/dev/null || sudo chmod +x "$target"
    say "installed $target"
}

main() {
    if [ "$FROM_SOURCE" -eq 1 ]; then
        build_from_source
    else
        platform="$(detect_platform)"
        asset="cppm_${platform}.tar.gz"
        if [ "$VERSION" = "latest" ]; then
            base="https://github.com/$REPO/releases/latest/download"
        else
            base="https://github.com/$REPO/releases/download/$VERSION"
        fi

        tmp="$(mktemp -d)"
        trap 'rm -rf "$tmp"' EXIT

        say "downloading $asset ($VERSION)"
        if ! fetch "$base/$asset" "$tmp/$asset"; then
            warn "no release asset for $platform; falling back to building from source"
            build_from_source
            post_install
            return
        fi
        if fetch "$base/checksums.txt" "$tmp/checksums.txt" 2>/dev/null; then
            verify_checksum "$tmp/$asset" "$tmp/checksums.txt" "$asset"
        else
            warn "could not download checksums.txt; skipping verification"
        fi
        tar -xzf "$tmp/$asset" -C "$tmp" || die "could not extract $asset"
        [ -f "$tmp/cppm" ] || die "archive did not contain a cppm binary"
        install_binary "$tmp/cppm"
    fi
    post_install
}

post_install() {
    # Tell the user if the install directory is not on PATH, rather than letting
    # "command not found" be their first experience of cppm.
    case ":$PATH:" in
        *":$INSTALL_DIR:"*) ;;
        *)
            say ""
            warn "$INSTALL_DIR is not on your PATH. Add this to your shell profile:"
            say "    export PATH=\"$INSTALL_DIR:\$PATH\""
            ;;
    esac

    say ""
    if [ -x "$INSTALL_DIR/cppm" ]; then
        "$INSTALL_DIR/cppm" --version || true
    fi
    say ""
    say "next steps:"
    say "    cppm doctor       check your toolchain"
    say "    cppm init MyApp   create a project"
}

main
