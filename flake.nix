{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = inputs @ {
    nixpkgs,
    flake-parts,
    flake-utils,
    ...
  }:
    flake-parts.lib.mkFlake {inherit inputs;} {
      systems = flake-utils.lib.defaultSystems;
      perSystem = {pkgs, ...}: let
        desktopItem = pkgs.makeDesktopItem {
          name = "kage";
          desktopName = "Kage";
          comment = "TUI XMPP client";
          exec = "kage";
          terminal = true;
          categories = ["Network" "Chat" "InstantMessaging"];
        };
      in {
        packages.default = pkgs.buildGoModule {
          pname = "kage";
          version = "0.0.2";
          src = pkgs.lib.cleanSource ./.;
          vendorHash = "sha256-zPPta7z7u1Fo/r28cWP8kOrIhqvroZpV4QFfjRxSLu0=";

          env.CGO_ENABLED = 0;

          nativeCheckInputs = [
            pkgs.gnupg
          ];

          # notify-send (libnotify) is what notifyd shells out to for desktop
          # notifications — wrap it onto PATH so it's found regardless of
          # what's installed system-wide.
          nativeBuildInputs = [pkgs.makeWrapper];

          postInstall = ''
            mkdir -p $out/share/applications
            cp ${desktopItem}/share/applications/*.desktop $out/share/applications/
          '';

          postFixup = ''
            wrapProgram $out/bin/kage --prefix PATH : ${pkgs.lib.makeBinPath [pkgs.libnotify]}
          '';
        };

        # `nix develop` gives you `prosody`/`prosodyctl`/`openssl` for
        # devtest/prosody/ (a throwaway local XMPP server used to exercise
        # the xmpp/ package against a real server — see devtest/prosody/README.md).
        devShells.default = pkgs.mkShell {
          packages = [pkgs.go pkgs.prosody pkgs.openssl];
        };
      };
    };
}
