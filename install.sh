#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./install.sh [INSTALL_DIR]

Build Aether from source and install the binary.
Default installation directory: ~/.local/bin

Requires Go (see go.mod), Node.js >= 20.9, and npm.
Example: ./install.sh /usr/local/bin
EOF
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  usage
  exit 0
fi
if [[ "$#" -gt 1 || ( "$#" -eq 1 && -z "$1" ) ]]; then
  usage >&2
  exit 1
fi

repository_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
install_dir="${1:-${HOME}/.local/bin}"

for required_command in go node npm install mktemp; do
  if ! command -v "${required_command}" >/dev/null 2>&1; then
    printf 'Required command is missing: %s\n' "${required_command}" >&2
    exit 1
  fi
done

node -e '
  const [major, minor] = process.versions.node.split(".").map(Number);
  if (major < 20 || (major === 20 && minor < 9)) {
    console.error("Node.js 20.9 or newer is required.");
    process.exit(1);
  }
'

mkdir -p -- "${install_dir}"
install_dir="$(cd -- "${install_dir}" && pwd)"
if [[ -d "${install_dir}/aether" ]]; then
  printf 'Installation target is a directory: %s/aether\n' "${install_dir}" >&2
  exit 1
fi

temporary_binary=""
temporary_install=""
cleanup() {
  [[ -z "${temporary_binary}" ]] || rm -f -- "${temporary_binary}"
  [[ -z "${temporary_install}" ]] || rm -f -- "${temporary_install}"
}
trap cleanup EXIT

printf 'Building frontend...\n'
(
  cd -- "${repository_root}/web"
  npm ci --no-audit --no-fund
  NEXT_TELEMETRY_DISABLED=1 npm run build
)

printf 'Building Aether...\n'
mkdir -p -- "${repository_root}/dist"
temporary_binary="$(mktemp "${repository_root}/dist/.aether.XXXXXX")"
source_commit="unknown"
if command -v git >/dev/null 2>&1 && git -C "${repository_root}" rev-parse --verify HEAD >/dev/null 2>&1; then
  source_commit="$(git -C "${repository_root}" rev-parse --verify HEAD)"
fi
built_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
(
  cd -- "${repository_root}"
  CGO_ENABLED=0 go build -buildvcs=false -trimpath -tags embedstatic \
    -ldflags "-s -w -X github.com/C1ph3rX13/Aether/internal/version.Commit=${source_commit} -X github.com/C1ph3rX13/Aether/internal/version.BuiltAt=${built_at}" \
    -o "${temporary_binary}" ./cmd/aether
)
chmod 0755 "${temporary_binary}"
"${temporary_binary}" --version
mv -f -- "${temporary_binary}" "${repository_root}/dist/aether"
temporary_binary=""

temporary_install="$(mktemp "${install_dir}/.aether.XXXXXX")"
install -m 0755 "${repository_root}/dist/aether" "${temporary_install}"
mv -f -- "${temporary_install}" "${install_dir}/aether"
temporary_install=""

printf '\nInstalled Aether: %s/aether\n' "${install_dir}"
case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *) printf 'Add this directory to PATH: %s\n' "${install_dir}" ;;
esac
printf 'Configure a deployment manifest using configs/bootstrap-deployment.example.toml, then run:\n'
printf '  %q serve --deployment /path/to/deployment.toml\n' "${install_dir}/aether"
