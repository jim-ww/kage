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
      perSystem = {pkgs, ...}: {
        packages.default = pkgs.buildGoModule {
          pname = "kage";
          version = "1.0";
          src = pkgs.lib.cleanSource ./.;
          vendorHash = "sha256-qtmnwBl5Ex50lAXow5suLrqqlNqDQJcIDvoAvweQnhw=";
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
