{ pkgs ? import <nixpkgs> { } }:

pkgs.mkShell {
  buildInputs = [
    pkgs.go
    pkgs.tailwindcss
    pkgs.llama-cpp-vulkan
    pkgs.sqlite
  ];
}
