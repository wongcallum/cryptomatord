# End-to-end NixOS VM test: enables the module as a systemd user service and
# drives it with ctl. The fake cryptomator-cli performs a real (unprivileged)
# FUSE mount via bindfs, so the daemon's genuine isMountpoint() and
# SIGINT-unmount code paths run for real.
#
# The module deliberately provides no config, so this test writes the daemon's
# config to ~/.config/cryptomatord/config.json (where the daemon reads it),
# mirroring what the home-manager module does.
{ testers, writeShellApplication, bindfs, formats, module }:
let
  fakeCli = writeShellApplication {
    name = "cryptomator-cli";
    runtimeInputs = [ bindfs ];
    text = ''
      mountpoint=""
      src=""
      for arg in "$@"; do
        case "$arg" in
          unlock) : ;;
          --mountPoint=*) mountpoint="''${arg#--mountPoint=}" ;;
          -*) : ;;              # ignore --mounter, --password:stdin, etc.
          *) src="$arg" ;;      # positional vault path
        esac
      done
      cat >/dev/null || true     # consume the passphrase from stdin
      export FUSERMOUNT_PROG=/run/wrappers/bin/fusermount3
      exec bindfs -f --no-allow-other "$src" "$mountpoint"
    '';
  };

  # The config the daemon reads from the XDG config dir. autoMount is off so the
  # test drives mounting explicitly.
  configFile = (formats.json { }).generate "cryptomatord.json" {
    cliPath = "${fakeCli}/bin/cryptomator-cli";
    vaults.work = {
      path = "/home/tester/vaultsrc";
      mountPoint = "/home/tester/mnt/work";
      passwordCommand = "printf hunter2";
      autoMount = false;
    };
  };
in
testers.nixosTest {
  name = "cryptomatord";

  nodes.machine = { pkgs, ... }: {
    imports = [ module ];

    users.users.tester = {
      isNormalUser = true;
      uid = 1000;
      linger = true; # start the user's systemd instance at boot
    };

    boot.kernelModules = [ "fuse" ];
    # setuid fusermount3 so the unprivileged bindfs mount can attach.
    security.wrappers.fusermount3 = {
      source = "${pkgs.fuse3}/bin/fusermount3";
      owner = "root";
      group = "root";
      setuid = true;
    };

    services.cryptomatord = {
      enable = true;
      cliPackage = fakeCli;
      logLevel = "debug";
    };

    # Drop the daemon's config into the XDG config dir (the module no longer
    # provides it). systemd-tmpfiles creates the parent dirs and symlinks it in.
    systemd.tmpfiles.rules = [
      "d /home/tester/.config 0755 tester users -"
      "d /home/tester/.config/cryptomatord 0755 tester users -"
      "L+ /home/tester/.config/cryptomatord/config.json - - - - ${configFile}"
    ];
  };

  testScript = ''
    def user(cmd):
        return f"su tester -c 'export XDG_RUNTIME_DIR=/run/user/1000; export PATH=/run/current-system/sw/bin:$PATH; {cmd}'"

    machine.wait_for_unit("multi-user.target")

    # Vault fixture the fake cli will bind-mount.
    machine.succeed(
        "mkdir -p /home/tester/vaultsrc",
        "echo topsecret > /home/tester/vaultsrc/hello.txt",
        "chown -R tester:users /home/tester/vaultsrc",
    )

    # The user service should come up (via linger) and create the socket.
    machine.wait_for_unit("cryptomatord.service", "tester")
    machine.wait_until_succeeds(user("cryptomatord ctl status --json"), timeout=30)

    # Initially the vault is not mounted.
    status = machine.succeed(user("cryptomatord ctl status --json"))
    assert '"state":"unmounted"' in status, status

    # Mount it — ctl blocks until mounted and exits 0. The mount is private to
    # tester (no allow_other), so all mount observations run as tester too.
    machine.succeed(user("cryptomatord ctl mount work"))
    machine.wait_until_succeeds(user("mountpoint -q /home/tester/mnt/work"), timeout=15)
    # The cleartext is readable through the mount.
    machine.succeed(user("grep -q topsecret /home/tester/mnt/work/hello.txt"))

    status = machine.succeed(user("cryptomatord ctl status --json"))
    assert '"state":"mounted"' in status, status

    # Unmount it cleanly.
    machine.succeed(user("cryptomatord ctl unmount work"))
    machine.wait_until_fails(user("mountpoint -q /home/tester/mnt/work"), timeout=15)

    # Restarting the service should tear the mount down without orphaning it.
    machine.succeed(user("cryptomatord ctl mount work"))
    machine.wait_until_succeeds(user("mountpoint -q /home/tester/mnt/work"), timeout=15)
    machine.succeed("systemctl --user -M tester@ restart cryptomatord.service")
    machine.wait_until_fails(user("mountpoint -q /home/tester/mnt/work"), timeout=30)
  '';
}
