#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

upload="$1"
expected="$2"
[[ "$upload" =~ ^/tmp/agentpack-deploy\.[A-Za-z0-9]{8}$ && "$expected" =~ ^[a-f0-9]{64}$ ]]
[[ "$(uname -s)" == Linux && "$(uname -m)" == aarch64 ]]
for tool in systemctl loginctl curl sha256sum flock install; do command -v "$tool" >/dev/null; done
[[ "$(loginctl show-user "$(id -u)" -p Linger --value)" == yes ]] || {
  echo 'Enable persistent user services once: sudo loginctl enable-linger "$USER"' >&2; exit 1;
}
base="$HOME/.local/lib/agentpack"
state="$HOME/.local/share/agentpack-gateway"
unit="$HOME/.config/systemd/user/agentpack-gateway.service"
install -d -m 0700 "$base" "$state" "$(dirname "$unit")"
exec 9>"$base/deploy.lock"
flock -n 9 || { echo 'Another Agentpack deployment is running.' >&2; exit 1; }
work="$(mktemp -d "$base/install.XXXXXXXX")"
trap 'rm -rf -- "$work"' EXIT
install -m 0755 "$upload/agentpack" "$work/agentpack"
printf '%s  %s\n' "$expected" "$work/agentpack" | sha256sum --check --status
binary="$base/agentpack"
cat >"$work/agentpack-gateway.service" <<'UNIT'
[Unit]
Description=Agentpack public gateway
After=network-online.target

[Service]
Type=simple
WorkingDirectory=%h/.local/share/agentpack-gateway
Environment="AGENTPACK_HOME=%h/.local/share/agentpack-gateway"
Environment=AGENTPACK_PUBLIC_URL=https://agentpack.microapps.space
ExecStart=%h/.local/lib/agentpack/agentpack gateway serve --listen 127.0.0.1:7420
Restart=on-failure
RestartSec=3
TimeoutStopSec=15
KillMode=process
UMask=0077
NoNewPrivileges=true

[Install]
WantedBy=default.target
UNIT
was_active=false
was_enabled=false
systemctl --user is-active --quiet agentpack-gateway.service && was_active=true
systemctl --user is-enabled --quiet agentpack-gateway.service && was_enabled=true
for path in "$binary" "$unit"; do
  if [[ -e "$path" ]]; then cp -p "$path" "$work/$(basename "$path").before"; fi
done
rollback() {
  trap - ERR
  echo 'Deployment failed; restoring the previous binary and service configuration.' >&2
  systemctl --user stop agentpack-gateway.service || true
  for path in "$binary" "$unit"; do
    if [[ -f "$work/$(basename "$path").before" ]]; then
      cp -p "$work/$(basename "$path").before" "$path"
    else
      rm -f -- "$path"
    fi
  done
  systemctl --user daemon-reload || true
  if "$was_active"; then systemctl --user start agentpack-gateway.service || true; fi
  if ! "$was_enabled"; then systemctl --user disable agentpack-gateway.service || true; fi
  exit 1
}
trap rollback ERR
install -m 0755 "$work/agentpack" "$base/agentpack.new"
mv -f "$base/agentpack.new" "$binary"
install -m 0600 "$work/agentpack-gateway.service" "$unit"
systemctl --user daemon-reload
systemctl --user enable agentpack-gateway.service
systemctl --user restart agentpack-gateway.service
healthy=false
for attempt in {1..30}; do
  if systemctl --user is-active --quiet agentpack-gateway.service && curl --fail --silent --max-time 2 -H 'Host: agentpack.microapps.space' http://127.0.0.1:7420/api/status | grep -q 'setup_required'; then healthy=true; break; fi
  sleep 1
done
"$healthy"
curl --fail --silent --max-time 20 https://agentpack.microapps.space/api/status | grep -q 'setup_required'
if [[ -f "$work/agentpack.before" ]]; then install -m 0755 "$work/agentpack.before" "$base/agentpack.previous"; fi
trap - ERR
echo 'Gateway installed; local service and public HTTPS checked. Existing Caddy configuration retained.'
