#!/bin/sh
set -eu

REPOSITORY="nistroai/ctl"
VERSION="${NISTRO_VERSION:-}"
INSTALL_DIR="${NISTRO_INSTALL_DIR:-${HOME}/.nistro/bin}"
MODIFY_PATH=1

usage() {
	cat <<'EOF'
Install the Nistro CLI from GitHub Releases.

Usage: install.sh [options]

Options:
  --version <version>      Install a release such as v0.1.0 (default: latest)
  --install-dir <path>     Installation directory (default: ~/.nistro/bin)
  --no-modify-path         Do not update the shell profile
  -h, --help               Show this help
EOF
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--version)
			[ "$#" -ge 2 ] || { echo "--version requires a value" >&2; exit 2; }
			VERSION=$2
			shift 2
			;;
		--install-dir)
			[ "$#" -ge 2 ] || { echo "--install-dir requires a value" >&2; exit 2; }
			INSTALL_DIR=$2
			shift 2
			;;
		--no-modify-path)
			MODIFY_PATH=0
			shift
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			echo "unknown option: $1" >&2
			usage >&2
			exit 2
			;;
	esac
done

case "$(uname -s)" in
	Linux*) OS=linux ;;
	Darwin*) OS=darwin ;;
	MINGW*|MSYS*|CYGWIN*) OS=windows ;;
	*) echo "unsupported operating system: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
	x86_64|amd64) ARCH=amd64 ;;
	arm64|aarch64) ARCH=arm64 ;;
	*) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

BINARY="nistro-${OS}-${ARCH}"
if [ "$OS" = windows ]; then
	BINARY="${BINARY}.exe"
	ASSET="${BINARY}.zip"
	DESTINATION="${INSTALL_DIR}/nistro.exe"
else
	ASSET="${BINARY}.tar.gz"
	DESTINATION="${INSTALL_DIR}/nistro"
fi

if [ -n "${NISTRO_RELEASE_URL:-}" ]; then
	RELEASE_URL=${NISTRO_RELEASE_URL}
elif [ -n "$VERSION" ]; then
	RELEASE_URL="https://github.com/${REPOSITORY}/releases/download/${VERSION}"
else
	RELEASE_URL="https://github.com/${REPOSITORY}/releases/latest/download"
fi

command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 1; }
TEMP_DIR=$(mktemp -d 2>/dev/null || mktemp -d -t nistro-install)
trap 'rm -rf "$TEMP_DIR"' EXIT INT TERM

echo "Downloading ${ASSET}..."
curl -fsSL "${RELEASE_URL}/${ASSET}" -o "${TEMP_DIR}/${ASSET}"
curl -fsSL "${RELEASE_URL}/SHA256SUMS" -o "${TEMP_DIR}/SHA256SUMS"

EXPECTED=$(awk -v asset="$ASSET" '$2 == asset || $2 == "*" asset { print $1; exit }' "${TEMP_DIR}/SHA256SUMS")
[ -n "$EXPECTED" ] || { echo "checksum for ${ASSET} was not found" >&2; exit 1; }

if command -v sha256sum >/dev/null 2>&1; then
	ACTUAL=$(sha256sum "${TEMP_DIR}/${ASSET}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
	ACTUAL=$(shasum -a 256 "${TEMP_DIR}/${ASSET}" | awk '{print $1}')
elif command -v openssl >/dev/null 2>&1; then
	ACTUAL=$(openssl dgst -sha256 "${TEMP_DIR}/${ASSET}" | awk '{print $NF}')
elif command -v certutil >/dev/null 2>&1; then
	ACTUAL=$(certutil -hashfile "${TEMP_DIR}/${ASSET}" SHA256 | sed -n '2p' | tr -d ' \r' | tr 'A-F' 'a-f')
else
	echo "a SHA256 utility is required" >&2
	exit 1
fi

[ "$ACTUAL" = "$EXPECTED" ] || { echo "checksum verification failed for ${ASSET}" >&2; exit 1; }

if [ "$OS" = windows ]; then
	command -v unzip >/dev/null 2>&1 || { echo "unzip is required" >&2; exit 1; }
	unzip -q "${TEMP_DIR}/${ASSET}" -d "$TEMP_DIR"
else
	tar -xzf "${TEMP_DIR}/${ASSET}" -C "$TEMP_DIR"
fi

mkdir -p "$INSTALL_DIR"
cp "${TEMP_DIR}/${BINARY}" "$DESTINATION"
if [ "$OS" != windows ]; then
	chmod 0755 "$DESTINATION"
fi

case ":${PATH}:" in
	*":${INSTALL_DIR}:"*) IN_PATH=1 ;;
	*) IN_PATH=0 ;;
esac

if [ "$IN_PATH" -eq 0 ] && [ "$MODIFY_PATH" -eq 1 ] && [ "$OS" != windows ]; then
	SHELL_NAME=$(basename "${SHELL:-sh}")
	case "$SHELL_NAME" in
		zsh) PROFILE="${ZDOTDIR:-$HOME}/.zshrc" ;;
		bash)
			if [ "$OS" = darwin ]; then PROFILE="$HOME/.bash_profile"; else PROFILE="$HOME/.bashrc"; fi
			;;
		*) PROFILE="$HOME/.profile" ;;
	esac
	touch "$PROFILE"
	if ! grep -Fq '# Nistro CLI' "$PROFILE"; then
		printf '\n# Nistro CLI\nexport PATH="$PATH:%s"\n' "$INSTALL_DIR" >> "$PROFILE"
		echo "Added ${INSTALL_DIR} to PATH in ${PROFILE}."
	fi
fi

echo "Installed Nistro CLI at ${DESTINATION}."
if [ "$IN_PATH" -eq 0 ]; then
	echo "Add ${INSTALL_DIR} to PATH or restart your shell."
fi
