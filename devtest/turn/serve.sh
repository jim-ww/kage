#!/usr/bin/env bash
# Runs a throwaway local coturn STUN/TURN server in the foreground, bound to
# 127.0.0.1 only. Ctrl-C to stop. Needs `nix develop` for the turnserver
# binary - see README.md.
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec turnserver -c "$dir/turnserver.conf"
