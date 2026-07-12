{
  description = "cryptomatord — a supervisor daemon for cryptomator-cli vault mounts";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    { self, nixpkgs, flake-utils }:
    let
      # Linux-only: the daemon depends on FUSE and drives systemd user services.
      systems = [ "x86_64-linux" "aarch64-linux" ];
    in
    flake-utils.lib.eachSystem systems (
      system:
      let
        pkgs = import nixpkgs { inherit system; };
        cryptomatord = pkgs.callPackage ./nix/package.nix { };
      in
      {
        packages = {
          default = cryptomatord;
          cryptomatord = cryptomatord;
        };

        devShells.default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.gopls
            pkgs.gotools # goimports
            pkgs.golangci-lint
            pkgs.cryptomator-cli # for manual smoke tests
            pkgs.fuse3
          ];
        };

        checks = {
          # Compile the daemon.
          package = cryptomatord;

          # Run the Go unit tests (config, supervisor state machine, api+client).
          gotest = pkgs.stdenv.mkDerivation {
            name = "cryptomatord-gotest";
            src = ./.;
            nativeBuildInputs = [ pkgs.go pkgs.fuse3 pkgs.bash ];
            configurePhase = ''
              export HOME=$TMPDIR
              export GOCACHE=$TMPDIR/go-cache
              export GOPROXY=off
              export GOFLAGS=-mod=mod
            '';
            buildPhase = ''
              patchShebangs test
              go test ./...
            '';
            installPhase = "touch $out";
            doCheck = false;
          };

          # Boot a VM exercising the NixOS module end-to-end.
          integration = pkgs.callPackage ./nixos-test.nix {
            module = self.nixosModules.default;
          };
        };
      }
    )
    // {
      nixosModules.default = import ./nix/module.nix self;
      overlays.default = final: _prev: {
        cryptomatord = final.callPackage ./nix/package.nix { };
      };
    };
}
