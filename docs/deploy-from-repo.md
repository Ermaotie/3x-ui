# Deploy from this repository

This project can be installed from either a GitHub release asset or a local
package built from a checkout. The local package path is the easiest way to test
a fork before publishing a release.

## Build a Linux package

On the build host:

```bash
git clone https://github.com/OWNER/3x-ui.git
cd 3x-ui
./deploy/package-release.sh
```

The package is written to `release/x-ui-linux-$ARCH.tar.gz`. By default `$ARCH`
is detected from the current machine. To build a specific architecture:

```bash
XUI_ARCH=amd64 ./deploy/package-release.sh
XUI_ARCH=arm64 ./deploy/package-release.sh
```

The script needs `curl`, `go`, `npm`, `tar`, and `unzip`. It downloads the same
runtime sidecar files used by the release workflow: Xray, geosite/geoip data, and
mtg where available for the target architecture.

If Go or npm is not on `PATH`, pass `GO_BIN=/path/to/go` or
`NPM_BIN=/path/to/npm`.

## Install a local package

Copy or build the package on the target server, then run:

```bash
sudo XUI_NONINTERACTIVE=1 \
  XUI_SSL_MODE=none \
  XUI_DB_TYPE=sqlite \
  XUI_PANEL_PORT=2096 \
  XUI_ENABLE_FAIL2BAN=false \
  ./install.sh ./release/x-ui-linux-amd64.tar.gz
```

For arm64, replace the package name with `x-ui-linux-arm64.tar.gz`.

Local package mode is self-contained for the service registration path: it uses
the `x-ui.sh` bundled in the package and writes the systemd/OpenRC service file
itself. The target server does not need to reach GitHub during this install.

After installation:

```bash
sudo systemctl status x-ui --no-pager
sudo cat /etc/x-ui/install-result.env
```

`/etc/x-ui/install-result.env` contains the generated panel URL, username, and
password when unattended install is used.

## Install from a fork release

Publish `x-ui-linux-$ARCH.tar.gz` to the fork's GitHub Releases, then run this
on the target server:

```bash
curl -Ls https://raw.githubusercontent.com/OWNER/3x-ui/main/install.sh -o /tmp/install.sh
sudo XUI_REPO=OWNER/3x-ui \
  XUI_NONINTERACTIVE=1 \
  XUI_SSL_MODE=none \
  XUI_DB_TYPE=sqlite \
  XUI_PANEL_PORT=2096 \
  XUI_ENABLE_FAIL2BAN=false \
  bash /tmp/install.sh
```

`XUI_REPO` controls both the release asset download and the fallback script
download paths.

## Quick faucet setup

The faucet is configured from the panel under the subscription settings. It can
run even when normal subscription endpoints are disabled. Users receive only a
time-limited client proxy link, a QR code, and parsed client configuration; no
subscription link is shown.

For a public faucet, configure:

- Faucet enabled.
- Faucet domain and path, for example `https://example.com` and `/index/`.
- Faucet inbound/client defaults.
- Traffic in MB, expiry, IP cooldown, daily limits, and optional Turnstile keys.

The faucet listener uses the same public subscription listener port configured
in that settings page.

For Cloudflare orange-cloud proxying, use a VLESS WebSocket inbound behind
Nginx/Caddy on port 443. Bind the Xray inbound to localhost, proxy the WebSocket
path to that inbound, and proxy the panel/faucet HTTP paths to the x-ui panel.
Use an external proxy override so the generated faucet link shows
`host:443`, `type=ws`, and `security=tls`.
