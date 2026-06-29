#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root_dir}"

arch() {
    case "$(uname -m)" in
        x86_64 | x64 | amd64) echo "amd64" ;;
        i*86 | x86) echo "386" ;;
        armv8* | armv8 | arm64 | aarch64) echo "arm64" ;;
        armv7* | armv7 | arm) echo "armv7" ;;
        armv6* | armv6) echo "armv6" ;;
        armv5* | armv5) echo "armv5" ;;
        s390x) echo "s390x" ;;
        *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;
    esac
}

need() {
    command -v "$1" > /dev/null 2>&1 || { echo "missing command: $1" >&2; exit 1; }
}

platform="${XUI_ARCH:-$(arch)}"
out_dir="${OUT_DIR:-${root_dir}/release}"
xray_version="${XRAY_VERSION:-v26.6.27}"
mtg_version="${MTG_VERSION:-2.2.8}"
go_bin="${GO_BIN:-go}"
npm_bin="${NPM_BIN:-npm}"

need curl
need "${go_bin}"
need "${npm_bin}"
need tar
need unzip

case "${platform}" in
    amd64) xray_zip="Xray-linux-64.zip" ;;
    arm64) xray_zip="Xray-linux-arm64-v8a.zip" ;;
    armv7) xray_zip="Xray-linux-arm32-v7a.zip" ;;
    armv6) xray_zip="Xray-linux-arm32-v6.zip" ;;
    386) xray_zip="Xray-linux-32.zip" ;;
    armv5) xray_zip="Xray-linux-arm32-v5.zip" ;;
    s390x) xray_zip="Xray-linux-s390x.zip" ;;
    *) echo "unsupported package platform: ${platform}" >&2; exit 1 ;;
esac

if [[ ! -d frontend/node_modules ]]; then
    (cd frontend && "${npm_bin}" ci)
fi
(cd frontend && "${npm_bin}" run build)

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT
pkg_dir="${tmp_dir}/x-ui"
mkdir -p "${pkg_dir}/bin" "${out_dir}"

ldflags="${LDFLAGS:--w -s}"
"${go_bin}" build -ldflags "${ldflags}" -o "${pkg_dir}/x-ui" main.go
cp x-ui.sh x-ui.service.debian x-ui.service.arch x-ui.service.rhel "${pkg_dir}/"

curl -fL --retry 3 -o "${tmp_dir}/xray.zip" "https://github.com/XTLS/Xray-core/releases/download/${xray_version}/${xray_zip}"
mkdir -p "${tmp_dir}/xray"
unzip -q "${tmp_dir}/xray.zip" -d "${tmp_dir}/xray"
install -m 755 "${tmp_dir}/xray/xray" "${pkg_dir}/bin/xray-linux-${platform}"
[[ -f "${tmp_dir}/xray/LICENSE" ]] && cp "${tmp_dir}/xray/LICENSE" "${pkg_dir}/bin/"
[[ -f "${tmp_dir}/xray/README.md" ]] && cp "${tmp_dir}/xray/README.md" "${pkg_dir}/bin/"

curl -fL --retry 3 -o "${pkg_dir}/bin/geoip.dat" "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat"
curl -fL --retry 3 -o "${pkg_dir}/bin/geosite.dat" "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat"
curl -fL --retry 3 -o "${pkg_dir}/bin/geoip_IR.dat" "https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geoip.dat"
curl -fL --retry 3 -o "${pkg_dir}/bin/geosite_IR.dat" "https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geosite.dat"
curl -fL --retry 3 -o "${pkg_dir}/bin/geoip_RU.dat" "https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geoip.dat"
curl -fL --retry 3 -o "${pkg_dir}/bin/geosite_RU.dat" "https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geosite.dat"

case "${platform}" in
    amd64 | arm64 | armv7 | armv6 | 386)
        mtg_url="https://github.com/9seconds/mtg/releases/download/v${mtg_version}/mtg-${mtg_version}-linux-${platform}.tar.gz"
        curl -fL --retry 3 -o "${tmp_dir}/mtg.tar.gz" "${mtg_url}"
        mkdir -p "${tmp_dir}/mtg"
        tar -xzf "${tmp_dir}/mtg.tar.gz" -C "${tmp_dir}/mtg"
        mtg_bin="$(find "${tmp_dir}/mtg" -type f -name mtg -perm -111 | head -n 1)"
        [[ -n "${mtg_bin}" ]] && install -m 755 "${mtg_bin}" "${pkg_dir}/bin/mtg-linux-${platform}"
        ;;
esac

chmod +x "${pkg_dir}/x-ui" "${pkg_dir}/x-ui.sh" "${pkg_dir}/bin/xray-linux-${platform}"
out="${out_dir}/x-ui-linux-${platform}.tar.gz"
tar -C "${tmp_dir}" -zcf "${out}" x-ui

echo "package: ${out}"
sha256sum "${out}"
