#!/bin/sh
set -e

REPO="posit-dev/velocirepo"
INSTALL_DIR="${INSTALL_DIR:-./bin}"
# VELOCIREPO_BASE_URL can be overridden in tests to point at a local server.
BASE_URL="${VELOCIREPO_BASE_URL:-https://github.com}"

detect_os() {
    case "$(uname -s)" in
        Linux*)  echo "linux" ;;
        Darwin*) echo "darwin" ;;
        *)       echo "unsupported"; return 1 ;;
    esac
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)  echo "amd64" ;;
        arm64|aarch64) echo "arm64" ;;
        *)             echo "unsupported"; return 1 ;;
    esac
}

get_latest_version() {
    curl -sSfL -o /dev/null -w '%{url_effective}' "${BASE_URL}/${REPO}/releases/latest" |
        grep -o '[^/]*$' |
        sed 's/^v//'
}

sha256_file() {
    # Portable SHA-256: prefer sha256sum (Linux, busybox), fall back to shasum (macOS).
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -d' ' -f1
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | cut -d' ' -f1
    else
        echo "Error: neither sha256sum nor shasum is available" >&2
        return 1
    fi
}

OS=$(detect_os)
ARCH=$(detect_arch)

if [ "$OS" = "unsupported" ] || [ "$ARCH" = "unsupported" ]; then
    echo "Error: unsupported platform $(uname -s)/$(uname -m)" >&2
    echo "See https://github.com/${REPO}/releases for available binaries." >&2
    exit 1
fi

if [ -n "$VERSION" ]; then
    VERSION="${VERSION#v}"
else
    echo "Fetching latest version..." >&2
    VERSION=$(get_latest_version)
fi

if [ -z "$VERSION" ]; then
    echo "Error: could not determine latest version" >&2
    exit 1
fi

ARTIFACT="velocirepo_${VERSION}_${OS}_${ARCH}.tar.gz"
URL="${BASE_URL}/${REPO}/releases/download/v${VERSION}/${ARTIFACT}"
SUMS_URL="${BASE_URL}/${REPO}/releases/download/v${VERSION}/SHA256SUMS"

echo "Installing velocirepo v${VERSION} (${OS}/${ARCH})..." >&2
echo "  from: ${URL}" >&2

mkdir -p "$INSTALL_DIR"

WORKDIR=$(mktemp -d)
# shellcheck disable=SC2064
trap "rm -rf '$WORKDIR'" EXIT

ARCHIVE="${WORKDIR}/${ARTIFACT}"
SUMSFILE="${WORKDIR}/SHA256SUMS"

if ! curl -sSfL "$URL" -o "$ARCHIVE"; then
    echo "Error: download failed. Check that v${VERSION} exists for ${OS}/${ARCH}." >&2
    echo "  ${URL}" >&2
    exit 1
fi

if ! curl -sSfL "$SUMS_URL" -o "$SUMSFILE"; then
    echo "Error: could not download SHA256SUMS from ${SUMS_URL}" >&2
    exit 1
fi

EXPECTED=$(awk -v a="$ARTIFACT" '$2 == a {print $1}' "$SUMSFILE")
if [ -z "$EXPECTED" ]; then
    echo "Error: no checksum found for ${ARTIFACT} in SHA256SUMS" >&2
    exit 1
fi

ACTUAL=$(sha256_file "$ARCHIVE")
if [ "$ACTUAL" != "$EXPECTED" ]; then
    echo "Error: checksum mismatch for ${ARTIFACT}" >&2
    echo "  expected: ${EXPECTED}" >&2
    echo "  actual:   ${ACTUAL}" >&2
    exit 1
fi

if ! tar xz -C "$INSTALL_DIR" -f "$ARCHIVE" velocirepo; then
    echo "Error: extraction failed." >&2
    exit 1
fi

chmod +x "${INSTALL_DIR}/velocirepo"

echo "Installed: ${INSTALL_DIR}/velocirepo" >&2
"${INSTALL_DIR}/velocirepo" version
