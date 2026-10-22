#!/usr/bin/env bash
# Installs GSB Gateway for the current user: binary in ~/.local/bin, menu
# entry and icon, and (with --autostart) a login autostart entry. No root.
#   ./install.sh [--autostart]      ./install.sh --uninstall
set -euo pipefail
src="$(cd "$(dirname "$0")" && pwd)"
bin="$HOME/.local/bin"
apps="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
icons="${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor/128x128/apps"
autostart="${XDG_CONFIG_HOME:-$HOME/.config}/autostart"

if [ "${1:-}" = "--uninstall" ]; then
  rm -f "$bin/gsb-gateway" "$apps/gsb-gateway.desktop" "$icons/gsb-gateway.png" "$autostart/gsb-gateway.desktop"
  echo "Kaldırıldı. Ayarlar ~/.config/gsb-gateway içinde duruyor."
  exit 0
fi

mkdir -p "$bin" "$apps" "$icons"
install -m 755 "$src/gsb-gateway" "$bin/gsb-gateway"
install -m 644 "$src/gsb-gateway.png" "$icons/gsb-gateway.png"
sed "s|^Exec=.*|Exec=$bin/gsb-gateway|" "$src/gsb-gateway.desktop" > "$apps/gsb-gateway.desktop"
if [ "${1:-}" = "--autostart" ]; then
  mkdir -p "$autostart"
  sed "s|^Exec=.*|Exec=$bin/gsb-gateway --no-browser|" "$src/gsb-gateway.desktop" > "$autostart/gsb-gateway.desktop"
fi
echo "Kuruldu: $bin/gsb-gateway (uygulama menüsünde \"GSB Gateway\")"
