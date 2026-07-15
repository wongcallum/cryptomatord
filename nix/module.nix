# NixOS module for cryptomatord. Runs the daemon as a systemd *user* service so
# FUSE mounts land in the user's session and password commands can reach the
# user's gpg-agent / keyring.
#
# This module deliberately provides *no* configuration: the daemon reads
# ~/.config/cryptomatord/config.json (i.e. $XDG_CONFIG_HOME/cryptomatord/config.json).
# Write that file with the home-manager module (cryptomatord.homeModules.default),
# or by hand — see the README's Configuration section for the schema. Use this
# module or the home-manager service, not both.
#
# Usage:
#   imports = [ cryptomatord.nixosModules.default ];
#   services.cryptomatord = {
#     enable = true;
#     extraPackages = [ pkgs.pass pkgs.gnupg ];  # for the password command
#   };
self:
{ config, lib, pkgs, ... }:
let
  cfg = config.services.cryptomatord;
in
{
  options.services.cryptomatord = {
    enable = lib.mkEnableOption "the cryptomatord vault supervisor (runs as a systemd user service)";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "cryptomatord.packages.\${system}.default";
      description = "The cryptomatord package providing the daemon and ctl.";
    };

    cliPackage = lib.mkOption {
      type = lib.types.package;
      default = pkgs.cryptomator-cli;
      defaultText = lib.literalExpression "pkgs.cryptomator-cli";
      description = ''
        The cryptomator-cli package. Put on the daemon's PATH and in
        environment.systemPackages; the config file's cliPath is what the daemon
        actually execs.
      '';
    };

    logLevel = lib.mkOption {
      type = lib.types.enum [ "debug" "info" "warn" "error" ];
      default = "info";
      description = "Daemon log level.";
    };

    extraPackages = lib.mkOption {
      type = lib.types.listOf lib.types.package;
      default = [ ];
      example = lib.literalExpression "[ pkgs.pass pkgs.gnupg ]";
      description = "Extra packages on the daemon's PATH, e.g. tools used by passwordCommand.";
    };
  };

  config = lib.mkIf cfg.enable {
    # Put ctl and the CLI on PATH for the desktop widget and interactive use.
    environment.systemPackages = [ cfg.package cfg.cliPackage ];

    systemd.user.services.cryptomatord = {
      description = "cryptomatord — cryptomator-cli vault supervisor";
      # Ordered after the graphical session (not just default.target): the
      # daemon execs passwordCommand (e.g. zenity), which needs DISPLAY /
      # WAYLAND_DISPLAY. Those are imported into the user manager's
      # environment by the compositor as it brings up graphical-session.target;
      # starting any earlier freezes the daemon's own environment without them.
      # partOf restarts the daemon whenever the session cycles, so it always
      # inherits fresh values.
      after = [ "graphical-session.target" ];
      partOf = [ "graphical-session.target" ];
      wantedBy = [ "graphical-session.target" ];

      # NOTE: intentionally no PrivateMounts / ProtectSystem=strict etc. — the
      # FUSE mounts must remain visible to the rest of the user session.
      serviceConfig = {
        # No --config: the daemon defaults to $XDG_CONFIG_HOME/cryptomatord/config.json
        # (i.e. ~/.config/cryptomatord/config.json), provided out of band.
        ExecStart = "${cfg.package}/bin/cryptomatord serve --log-level ${cfg.logLevel}";
        Restart = "on-failure";
        RestartSec = 2;
        # Deliver SIGTERM only to the daemon, which then SIGINTs the
        # cryptomator-cli children for a clean unmount; SIGKILL the rest of the
        # cgroup only after the timeout.
        KillMode = "mixed";
        KillSignal = "SIGTERM";
        TimeoutStopSec = 90;
        RuntimeDirectory = "cryptomatord";
      };

      path = [ cfg.cliPackage pkgs.fuse3 pkgs.coreutils pkgs.bash ] ++ cfg.extraPackages;
    };
  };
}
