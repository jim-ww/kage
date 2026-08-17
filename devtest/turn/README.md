# Local test STUN/TURN server

A throwaway coturn instance for exercising `call/`'s ICE path against a real
STUN/TURN server, instead of only Google's public STUN
(`stun:stun.l.google.com:19302`, all `call/peer.go`'s `NewPeerConnection`
configures today - no TURN relay).

```sh
nix develop          # provides turnserver
./devtest/turn/serve.sh   # runs coturn in the foreground (Ctrl-C to stop)
```

Listens on `127.0.0.1:3478` (STUN and TURN, plain UDP, no TLS/DTLS), relay
ports `49160-49200`. Long-term credential: username `kage`, password
`kagepw`, realm `localhost`.

To point a call at it instead of (or alongside) the public STUN server, add
a second `webrtc.ICEServer` to `call.NewPeerConnection`'s `Configuration`,
e.g.:

```go
ICEServers: []webrtc.ICEServer{
	{URLs: []string{"stun:stun.l.google.com:19302"}},
	{
		URLs:       []string{"turn:127.0.0.1:3478"},
		Username:   "kage",
		Credential: "kagepw",
	},
},
```

Only useful for interactively poking at TURN relay behavior - the two
sandboxes this project's own tests run in structurally can't do real ICE at
all (`AF_NETLINK` is blocked; see `call/loopback_test.go`'s
`skipIfNoNetwork` and `call_e2e_test.go`'s live-call tests, which both skip
there), so nothing in the test suite currently depends on this server being
up. Run it if you're debugging a NAT/relay-specific call issue locally,
where real networking is available.
