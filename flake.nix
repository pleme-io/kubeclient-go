# flake.nix — kubeclient-go (GSDS Biblioteca) via substrate's go-library-flake.
# vendorHash OMITTED → spec-sourced (__from-spec__); the clean nix build lands
# once k8sauthconfig-go is published and the client-go vendor hash is computed.
# Pre-publish proof is `go test` (green).
{
  description = "kubeclient-go — the fleet's typed client-go kubeconfig/exec-credential + pod-exec/log/copy + ESO informer helper";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    substrate = {
      # Published repo uses: url = "github:pleme-io/substrate";
      url = "git+file:///Users/drzzln/code/github/pleme-io/substrate?ref=feat/go-pattern-parity";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = inputs @ { self, nixpkgs, substrate, ... }:
    (import substrate.goLibraryFlakeBuilder { inherit nixpkgs; }) {
      name = "kubeclient-go";
      version = "0.1.0";
      src = self;
      repo = "pleme-io/kubeclient-go";
    };
}
