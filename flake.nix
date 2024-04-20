{
  description = "A very basic flake";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs?ref=nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    templ = {
      url = "github:a-h/templ";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-utils.follows = "flake-utils";
    };
  };

  outputs = { self, nixpkgs, flake-utils, templ, ... }:
    flake-utils.lib.eachDefaultSystem
        (system:
          let
            overlays = [ templ.overlays.default ];
            pkgs = import nixpkgs {
              inherit system overlays;
            };
          in
          with pkgs;
          {
            devShells.default = mkShell {
              buildInputs = [
                go
                nodejs_18
                templ.packages.${system}.templ
                gnumake
                mpv
                yt-dlp
                imagemagick
              ];
            };
          }
        );
}
