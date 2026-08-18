# kage

A terminal (TUI) XMPP client written in Go, built on [Bubble Tea](https://charm.land/bubbletea/).

## Features

- Multi-account XMPP chat with a persistent background daemon (keeps you connected/notified while the TUI isn't running)
- End-to-end encryption: OMEMO (v1/v2) and GPG, selectable per chat
- Encrypted local message history (SQLite, optional password-derived key)
- Voice/video calls (Jingle signaling, WebRTC via pion) and screen sharing
- File transfer/attachments, message editing, replies, reactions, read receipts
- Message Archive Management (MAM) history sync, carbons, chat states
- Mouse support, themeable UI (Tokyo Night by default), configurable keybinds
- System tray icon and desktop notifications

## Install

Requires Go 1.26+.

```sh
go install github.com/jim-ww/kage@latest
```

Or try it with Nix:

```sh
nix run github:jim-ww/kage
```

Or add it to your flake inputs:

```nix
inputs.kage.url = "github:jim-ww/kage";
# environment.systemPackages = [ inputs.kage.packages.${system}.default ];
```

## Usage

```sh
kage                  # launch the TUI (opens the add-account form on first run)
kage export           # export accounts/data
kage import           # import accounts/data
kage daemon           # manage the background service
kage --config <path>  # use a specific config file
kage --debug          # debug logging to <config dir>/kage/debug.log
```

## Configuration

kage reads a YAML config (see [config.example.yaml](config.example.yaml) for every option, fully commented) from the default OS config directory unless `--config` is given. At minimum you need one account:

```yaml
accounts:
  - jid: user@example.com
    password_cmd: pass show xmpp/user
```

Passwords (account and local storage) resolve in order: OS keyring → `password_cmd` → plaintext `password`.

## Optional dependencies

- `mpv` — playing video (video calls)
- `wf-recorder` — screen sharing in calls
- `libnotify` — desktop notifications
- `wl-clipboard` / `xclip` — pasting images into chat

## Support the project

If kage is useful to you, consider a small donation.

**Monero (XMR)**

`83YGRqP8uHed6NeegZQeX9ccCxbzoRHHEEi7pTwk4aqdJZEVXXA6NWtetnsEM2v33zFBBt3Rp6DNhU9qhJEGPspU14yN8t7`

## License

GPL-3.0. Free to use, study, share, and modify — provided you keep the same freedoms for others.
