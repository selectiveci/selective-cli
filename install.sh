#!/bin/sh
set -e

REPO="selectiveci/selective-cli"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

detect_os() {
  case "$(uname -s)" in
    Darwin) echo "darwin" ;;
    Linux)  echo "linux" ;;
    *)      echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64)  echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;
  esac
}

latest_version() {
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' \
    | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/'
}

main() {
  OS=$(detect_os)
  ARCH=$(detect_arch)
  VERSION="${VERSION:-$(latest_version)}"
  BASENAME="selective_${VERSION#v}_${OS}_${ARCH}"
  ARCHIVE="${BASENAME}.tar.gz"
  URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
  CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT

  echo "Downloading selective ${VERSION} (${OS}/${ARCH})..."
  curl -fsSL "$URL" -o "${TMP}/${ARCHIVE}"
  curl -fsSL "$CHECKSUM_URL" -o "${TMP}/checksums.txt"

  cd "$TMP"
  if command -v sha256sum >/dev/null 2>&1; then
    grep "${ARCHIVE}" checksums.txt | sha256sum -c -
  elif command -v shasum >/dev/null 2>&1; then
    grep "${ARCHIVE}" checksums.txt | shasum -a 256 -c -
  else
    echo "Warning: cannot verify checksum (no sha256sum or shasum found)" >&2
  fi

  tar -xzf "${ARCHIVE}"
  install -m 755 selective "${INSTALL_DIR}/selective"
  echo "Installed selective ${VERSION} to ${INSTALL_DIR}/selective"
}

main "$@"
