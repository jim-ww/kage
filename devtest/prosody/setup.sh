#!/usr/bin/env bash
# Renders prosody.cfg.lua from the template, generates a self-signed cert for
# "localhost" if missing, and registers the alice@localhost/bob@localhost
# test accounts if they don't already exist. Idempotent — safe to rerun.
#
# Requires prosody/prosodyctl and openssl on PATH: `nix develop` in the repo
# root provides them.
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cfg="$dir/prosody.cfg.lua"

sed "s#@DIR@#$dir#g" "$dir/prosody.cfg.lua.tmpl" > "$cfg"

mkdir -p "$dir/certs" "$dir/data"

if [[ ! -f "$dir/certs/localhost.crt" ]]; then
	echo "generating self-signed cert for localhost..."
	openssl req -x509 -newkey ed25519 -nodes -days 3650 \
		-subj "/CN=localhost" \
		-addext "subjectAltName=DNS:localhost" \
		-keyout "$dir/certs/localhost.key" -out "$dir/certs/localhost.crt"
fi

for user in alice bob; do
	echo "registering $user@localhost (no-op if it already exists)..."
	PROSODY_CONFIG="$cfg" prosodyctl register "$user" localhost "${user}pw" || true
done

echo "done. run devtest/prosody/serve.sh to start the server."
