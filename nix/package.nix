{ lib, buildGoModule }:

buildGoModule {
  pname = "cryptomatord";
  version = "0.1.0";

  src = lib.fileset.toSource {
    root = ./..;
    # Only the Go sources are needed to build; keeps the store path small and
    # avoids rebuilds when nix/ or docs change.
    fileset = lib.fileset.unions [
      ../go.mod
      (lib.fileset.fileFilter (f: f.hasExt "go") ../.)
    ];
  };

  # stdlib-only: no module dependencies to vendor.
  vendorHash = null;

  # Build just the daemon binary from the module root.
  subPackages = [ "." ];

  ldflags = [ "-s" "-w" ];

  meta = {
    description = "Supervisor daemon that manages cryptomator-cli vault mounts";
    homepage = "https://github.com/callum/cryptomatord";
    license = lib.licenses.mit;
    mainProgram = "cryptomatord";
    platforms = lib.platforms.linux;
  };
}
