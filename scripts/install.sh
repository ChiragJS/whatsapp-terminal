#!/usr/bin/env sh
set -eu

REPO_OWNER="ChiragJS"
REPO_NAME="whatsapp-terminal"
BINARY_NAME="whatsapp-terminal"

detect_os() {
  case "$(uname -s)" in
    Linux) printf "Linux" ;;
    Darwin) printf "Darwin" ;;
    *)
      echo "unsupported OS: $(uname -s)" >&2
      exit 1
      ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf "x86_64" ;;
    arm64|aarch64) printf "arm64" ;;
    *)
      echo "unsupported architecture: $(uname -m)" >&2
      exit 1
      ;;
  esac
}

pick_install_dir() {
  if [ -n "${INSTALL_DIR:-}" ]; then
    printf "%s" "$INSTALL_DIR"
    return
  fi
  if [ -w /usr/local/bin ]; then
    printf "/usr/local/bin"
    return
  fi
  printf "%s/.local/bin" "$HOME"
}

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

checksum_cmd() {
  if command -v sha256sum >/dev/null 2>&1; then
    printf "sha256sum"
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    printf "shasum -a 256"
    return
  fi
  echo "missing checksum tool: sha256sum or shasum" >&2
  exit 1
}

OS="$(detect_os)"
ARCH="$(detect_arch)"
INSTALL_DIR="$(pick_install_dir)"
TAG="${RELEASE_TAG:-latest}"

need_cmd curl
need_cmd tar
SUM_CMD="$(checksum_cmd)"

if [ "$TAG" = "latest" ]; then
  BASE_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest/download"
else
  BASE_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/${TAG}"
fi

ARCHIVE_NAME="${BINARY_NAME}_${OS}_${ARCH}.tar.gz"
CHECKSUM_NAME="checksums.txt"

TMP_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

ARCHIVE_PATH="${TMP_DIR}/${ARCHIVE_NAME}"
CHECKSUM_PATH="${TMP_DIR}/${CHECKSUM_NAME}"

echo "Downloading ${ARCHIVE_NAME}..."
curl -fsSL "${BASE_URL}/${ARCHIVE_NAME}" -o "$ARCHIVE_PATH"
curl -fsSL "${BASE_URL}/${CHECKSUM_NAME}" -o "$CHECKSUM_PATH"

EXPECTED_SUM="$(grep " ${ARCHIVE_NAME}\$" "$CHECKSUM_PATH" | awk '{print $1}')"
if [ -z "$EXPECTED_SUM" ]; then
  echo "could not find checksum for ${ARCHIVE_NAME}" >&2
  exit 1
fi

ACTUAL_SUM="$($SUM_CMD "$ARCHIVE_PATH" | awk '{print $1}')"
if [ "$EXPECTED_SUM" != "$ACTUAL_SUM" ]; then
  echo "checksum mismatch for ${ARCHIVE_NAME}" >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"
tar -xzf "$ARCHIVE_PATH" -C "$TMP_DIR"
install -m 0755 "${TMP_DIR}/${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"

echo "Installed ${BINARY_NAME} to ${INSTALL_DIR}/${BINARY_NAME}"
if ! printf "%s" ":$PATH:" | grep -q ":${INSTALL_DIR}:"; then
  echo "Add ${INSTALL_DIR} to PATH if it is not already available in your shell."
fi
echo "Run: ${BINARY_NAME} --version"
