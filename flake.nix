{
  description = "local-inference-manager dev shell";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    # Upstream llama.cpp. It ships its own flake with a ready-made `vulkan`
    # package, so we consume that directly instead of re-deriving it.
    # `nix flake update llama-cpp` bumps to the latest commit.
    llama-cpp = {
      url = "github:ggml-org/llama.cpp";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, flake-utils, llama-cpp }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
      in {
        devShells.default = pkgs.mkShell {
          buildInputs = [
            pkgs.go
            pkgs.tailwindcss
            llama-cpp.packages.${system}.vulkan
            pkgs.sqlite
          ];
        };
      });
}
