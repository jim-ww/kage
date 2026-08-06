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
        commitRev = inputs.self.rev or inputs.self.dirtyRev or "unknown";
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
          version = "0.0.4";
          src = pkgs.lib.cleanSource ./.;
          vendorHash = "sha256-djmbnvqVKu9RPilXJtdKgXgieUqxOUdFnUidp7CwJMg=";

          env.CGO_ENABLED = 0;

          ldflags = ["-X github.com/jim-ww/kage/version.Version=0.0.4-${commitRev}"];

          doCheck = false;

          nativeCheckInputs = [
            pkgs.gnupg
          ];

          # notify-send (libnotify) is what notifyd shells out to for desktop
          # notifications; wl-paste/xclip back the PasteImage keybind (reads
          # a clipboard image directly, bypassing the terminal's bracketed
          # paste — see ui/clipboard_image.go). Wrap them onto PATH so
          # they're found regardless of what's installed system-wide.
          nativeBuildInputs = [pkgs.makeWrapper];

          postInstall = ''
            mkdir -p $out/share/applications
            cp ${desktopItem}/share/applications/*.desktop $out/share/applications/
          '';

          postFixup = ''
            wrapProgram $out/bin/kage --prefix PATH : ${pkgs.lib.makeBinPath [pkgs.libnotify pkgs.wl-clipboard pkgs.xclip]}
          '';
        };

        # `nix develop` gives you `prosody`/`prosodyctl`/`openssl` for
        # devtest/prosody/ (a throwaway local XMPP server used to exercise
        # the xmpp/ package against a real server — see devtest/prosody/README.md).
        devShells.default = pkgs.mkShell {
          packages = [pkgs.go pkgs.prosody pkgs.openssl pkgs.libnotify];
        };
      };
    };
}
