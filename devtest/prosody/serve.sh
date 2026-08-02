#!/usr/bin/env bash
# Runs the throwaway local prosody instance in the foreground. Ctrl-C to stop.
# Run setup.sh first (and whenever the template changes).
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec prosody --config "$dir/prosody.cfg.lua" -F
