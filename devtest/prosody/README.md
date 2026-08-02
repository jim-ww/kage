# Local test XMPP server

A throwaway Prosody instance for exercising `xmpp/` against a real server,
without needing a real account. Not for production use — self-signed cert,
plaintext auth storage, open registration.

```sh
nix develop            # provides prosody, prosodyctl, openssl
./devtest/prosody/setup.sh   # renders config, generates cert, registers test accounts
./devtest/prosody/serve.sh   # runs prosody in the foreground (Ctrl-C to stop)
```

Test accounts: `alice@localhost` / `alicepw`, `bob@localhost` / `bobpw`,
listening on `localhost:5222`. The cert is a self-signed one for `localhost`
at `certs/localhost.crt` — trust it explicitly in test code via a
`tls.Config{RootCAs: ...}` passed to `xmpp.Dial`, e.g.:

```go
certPEM, _ := os.ReadFile("devtest/prosody/certs/localhost.crt")
pool := x509.NewCertPool()
pool.AppendCertsFromPEM(certPEM)
client, err := xmpp.Dial(ctx, "alice@localhost", "alicepw",
	&tls.Config{ServerName: "localhost", RootCAs: pool})
```

Rerun `setup.sh` any time — it's idempotent (registering an existing account
is a no-op, the cert/config are only (re)generated if missing).

`data/`, `certs/`, `prosody.cfg.lua`, and `*.log`/`*.pid` are generated and
gitignored; only the template and scripts are committed.
