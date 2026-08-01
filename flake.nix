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
          vendorHash = "sha256-qvYjOa+Ojy5zt4jkCx8BJdd1uB87g3wnWdyt1RVXCgU=";
        };
      };
    };
}
