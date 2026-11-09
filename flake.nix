{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    inputs@{
      nixpkgs,
      flake-parts,
      flake-utils,
      ...
    }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = flake-utils.lib.defaultSystems;

      flake = {
        # Declarative config.toml generation for home-manager - see
        # nix/hm-module.nix for the full option list and why it only covers
        # config.toml's declarative half, not the runtime state.toml half
        # (sidebar width, last opened chat, ...) the app itself persists.
        homeManagerModules.default = import ./nix/hm-module.nix { inherit (inputs) self; };
      };

      perSystem =
        { pkgs, ... }:
        let
          commitRev = inputs.self.rev or inputs.self.dirtyRev or "unknown";
          desktopItem = pkgs.makeDesktopItem {
            name = "kage";
            desktopName = "Kage";
            comment = "TUI XMPP client";
            exec = "kage";
            terminal = true;
            categories = [
              "Network"
              "Chat"
              "InstantMessaging"
            ];
          };
        in
        {
          packages.default =
            let
              v = "0.9.0";
            in
            pkgs.buildGoModule {
              pname = "kage";
              version = v;
              src = pkgs.lib.cleanSource ./.;
              vendorHash = "sha256-IHhWWdSB4fONAdhWVguPk0fBtAHekeEFagTB0jSkq2g=";

              env.CGO_ENABLED = 1;

              ldflags = [ "-X github.com/jim-ww/kage/version.Version=${v}-${commitRev}" ];

              doCheck = false;

              nativeCheckInputs = [
                pkgs.gnupg
              ];

              # call/alsa_linux.go links libasound (`#cgo pkg-config: alsa`)
              # so miniaudio, which dlopens its audio backends, can always find
              # ALSA through the binary's RUNPATH. alsa-lib provides both the
              # headers and the .pc file pkg-config needs to find them.
              buildInputs = [ pkgs.alsa-lib ];

              # notify-send (libnotify) is what notifyd shells out to for desktop
              # notifications; wl-paste/xclip back the PasteImage keybind (reads
              # a clipboard image directly, bypassing the terminal's bracketed
              # paste — see ui/clipboard_image.go). Wrap them onto PATH so
              # they're found regardless of what's installed system-wide.
              nativeBuildInputs = [
                pkgs.makeWrapper
                pkgs.pkg-config
              ];

              postInstall = ''
                mkdir -p $out/share/applications
                cp ${desktopItem}/share/applications/*.desktop $out/share/applications/
              '';

              # libpulse is put on the binary's RUNPATH rather than linked:
              # miniaudio dlopens it when it can (proper device names for the
              # call menu's audio device picker) and falls back to ALSA when it
              # can't, so it mustn't become a hard dependency - and a RUNPATH
              # entry, unlike LD_LIBRARY_PATH on the wrapper, doesn't leak into
              # every process kage launches. Added here, after fixup's
              # patchELF has shrunk the RUNPATH down to what's actually linked.
              postFixup = ''
                patchelf --add-rpath ${pkgs.libpulseaudio}/lib $out/bin/kage
                wrapProgram $out/bin/kage --prefix PATH : ${
                  pkgs.lib.makeBinPath [
                    pkgs.libnotify
                    pkgs.wl-clipboard
                    pkgs.xclip
                    pkgs.wf-recorder
                    pkgs.mpv
                    # pw-record/pw-dump/pw-link: desktop audio while sharing
                    # the screen (call/desktopaudio.go)
                    pkgs.pipewire
                  ]
                }
              '';
            };

          # `nix develop` gives you `prosody`/`prosodyctl`/`openssl` for
          # devtest/prosody/ (a throwaway local XMPP server used to exercise
          # the xmpp/ package against a real server — see devtest/prosody/README.md)
          # and `turnserver` for devtest/turn/ (a throwaway coturn instance used
          # to exercise call/Jingle ICE against a real STUN/TURN server instead
          # of relying on Google's public one — see devtest/turn/README.md).
          devShells.default = pkgs.mkShell {
            packages = [
              pkgs.go
              pkgs.prosody
              pkgs.coturn
              pkgs.openssl
              pkgs.libnotify
              pkgs.alsa-lib
              pkgs.pkg-config
              pkgs.wf-recorder
              pkgs.mpv
            ];
            # Same RUNPATH entry the package adds in postFixup, for binaries
            # built here (`go run .`): lets miniaudio dlopen libpulse.
            CGO_LDFLAGS = "-O2 -g -Wl,-rpath,${pkgs.libpulseaudio}/lib";
          };

          # `nix run .#prosody-dev` / `.#turn-dev`: one-shot equivalents of the
          # nix develop + devtest/*/setup.sh + devtest/*/serve.sh dance above -
          # runtimeInputs puts prosody/prosodyctl/openssl (or turnserver) on
          # PATH for the duration of the script regardless of whatever's wrong
          # with the caller's own shell/PATH, which `nix develop` depends on.
          # Must be run from the repo root, same as the underlying scripts -
          # setup.sh/serve.sh resolve every path from their own location, but
          # still need cwd to be the working tree so devtest/*/certs,data,*.log
          # (gitignored, per-checkout state) land in the checkout, not some
          # nix store copy.
          apps.prosody-dev = {
            type = "app";
            program = "${
              pkgs.writeShellApplication {
                name = "prosody-dev";
                runtimeInputs = [
                  pkgs.prosody
                  pkgs.openssl
                ];
                text = ''
                  if [[ ! -f devtest/prosody/serve.sh ]]; then
                    echo "run this from the kage repo root (devtest/prosody/serve.sh not found under $PWD)" >&2
                    exit 1
                  fi
                  ./devtest/prosody/setup.sh
                  exec ./devtest/prosody/serve.sh
                '';
              }
            }/bin/prosody-dev";
          };

          apps.turn-dev = {
            type = "app";
            program = "${
              pkgs.writeShellApplication {
                name = "turn-dev";
                runtimeInputs = [ pkgs.coturn ];
                text = ''
                  if [[ ! -f devtest/turn/serve.sh ]]; then
                    echo "run this from the kage repo root (devtest/turn/serve.sh not found under $PWD)" >&2
                    exit 1
                  fi
                  exec ./devtest/turn/serve.sh
                '';
              }
            }/bin/turn-dev";
          };
        };
    };
}
