#!/bin/sh
set -e

REPO="selectiveci/selective-cli"
if [ -z "$INSTALL_DIR" ]; then
  if [ -w /usr/local/bin ]; then INSTALL_DIR=/usr/local/bin; else INSTALL_DIR="$HOME/.local/bin"; fi
fi

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

  echo "Downloading sel ${VERSION} (${OS}/${ARCH})..."
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
  mkdir -p "$INSTALL_DIR"
  install -m 755 sel "${INSTALL_DIR}/sel"
  echo "Installed sel ${VERSION} to ${INSTALL_DIR}/sel"
  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) echo "Add ${INSTALL_DIR} to your PATH to run sel" >&2 ;;
  esac
}

main "$@"
