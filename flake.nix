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
        runtimeDeps = [];
        kagePkg = pkgs.buildGoModule {
          pname = "kage";
          version = "1.0";
          src = pkgs.lib.cleanSource ./.;
          vendorHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

          buildInputs = runtimeDeps;
          nativeBuildInputs = [pkgs.go pkgs.makeWrapper] ++ runtimeDeps;
          nativeCheckInputs = [pkgs.go pkgs.makeWrapper] ++ runtimeDeps;

          buildPhase = ''
            go build -o kage .
          '';

          installPhase = ''
            install -Dm755 kage $out/bin/kage
            wrapProgram $out/bin/kage --prefix PATH : ${pkgs.lib.makeBinPath runtimeDeps}
          '';
        };
      in {
        packages.default = kagePkg;

        devShells.default = pkgs.mkShell {
          buildInputs = [pkgs.go] ++ runtimeDeps;
        };

        apps.default = flake-utils.lib.mkApp {
          drv = kagePkg;
        };
      };
    };
}
