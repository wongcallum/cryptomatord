# home-manager module for cryptomatord. Generates the daemon's config as a Nix
# store path, passes it to the daemon with --config, and runs it as a systemd
# *user* service, so FUSE mounts land in the user's session and password
# commands can reach the user's gpg-agent / keyring.
#
# This is the self-contained, per-user way to run cryptomatord. (The NixOS
# module is an alternative that runs the service system-side and reads config
# from ~/.config/cryptomatord/config.json instead — use one or the other, not
# both. The XDG config dir is for that module and for non-Nix setups.)
#
# Usage:
#   imports = [ cryptomatord.homeModules.default ];
#   services.cryptomatord = {
#     enable = true;
#     vaults.work = {
#       path = "/home/alice/Cloud/work";
#       mountPoint = "/home/alice/mnt/work";
#       passwordCommand = "pass show vaults/work";
#     };
#     extraPackages = [ pkgs.pass pkgs.gnupg ];  # for the password command
#   };
self:
{ config, lib, pkgs, ... }:
let
  cfg = config.services.cryptomatord;
  format = pkgs.formats.json { };

  vaultType = lib.types.submodule {
    options = {
      path = lib.mkOption {
        type = lib.types.str;
        description = "Absolute path to the vault directory (containing vault.cryptomator).";
      };
      mountPoint = lib.mkOption {
        type = lib.types.str;
        description = "Absolute path of an (ideally empty) directory to mount the cleartext at.";
      };
      passwordCommand = lib.mkOption {
        type = lib.types.str;
        example = "pass show vaults/work";
        description = "Shell command whose stdout is the vault passphrase.";
      };
      autoMount = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Mount this vault automatically when the daemon starts.";
      };
      mounter = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Override the mounter class for this vault (see `cryptomator-cli list-mounters`).";
      };
      mountOptions = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Extra mount options, passed through as repeated --mountOption flags.";
      };
    };
  };

  daemonConfig = {
    cliPath = "${cfg.cliPackage}/bin/cryptomator-cli";
    defaultMounter = cfg.defaultMounter;
    vaults = lib.mapAttrs
      (_name: v:
        { inherit (v) path mountPoint passwordCommand autoMount mountOptions; }
        // lib.optionalAttrs (v.mounter != null) { inherit (v) mounter; }
      )
      cfg.vaults;
  } // lib.optionalAttrs (cfg.socket != null) { socket = cfg.socket; };

  configFile = format.generate "cryptomatord.json" daemonConfig;
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
      description = "The cryptomator-cli package the daemon drives.";
    };

    socket = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        Control socket path. When null (the default) the daemon uses
        $XDG_RUNTIME_DIR/cryptomatord/control.sock, which ctl also defaults to.
        Only set this if you need a non-default location.
      '';
    };

    defaultMounter = lib.mkOption {
      type = lib.types.str;
      default = "org.cryptomator.frontend.fuse.mount.LinuxFuseMountProvider";
      description = "Default mounter class for vaults that don't set one.";
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

    vaults = lib.mkOption {
      type = lib.types.attrsOf vaultType;
      default = { };
      description = "Vaults to manage, keyed by name.";
    };
  };

  config = lib.mkIf cfg.enable {
    # ctl and the CLI on PATH for the desktop widget and interactive use.
    home.packages = [ cfg.package cfg.cliPackage ];

    # The daemon is pointed straight at the store-path config below (--config);
    # we deliberately do NOT write ~/.config/cryptomatord/config.json — that XDG
    # path is reserved for the NixOS module and non-Nix setups.
    systemd.user.services.cryptomatord = {
      Unit = {
        Description = "cryptomatord — cryptomator-cli vault supervisor";
        # Ordered after the graphical session (not just default.target): the
        # daemon execs passwordCommand (e.g. zenity), which needs DISPLAY /
        # WAYLAND_DISPLAY. Those are imported into the user manager's
        # environment by the compositor as it brings up graphical-session.target;
        # starting any earlier freezes the daemon's own environment without
        # them. PartOf restarts the daemon whenever the session cycles, so it
        # always inherits fresh values.
        After = "graphical-session.target";
        PartOf = "graphical-session.target";
      };

      # NOTE: intentionally no sandboxing (PrivateMounts etc.) — the FUSE mounts
      # must remain visible to the rest of the user session.
      Service = {
        ExecStart = "${cfg.package}/bin/cryptomatord serve --config ${configFile} --log-level ${cfg.logLevel}";
        Restart = "on-failure";
        RestartSec = 2;
        # Deliver SIGTERM only to the daemon, which then SIGINTs the
        # cryptomator-cli children for a clean unmount; SIGKILL the rest of the
        # cgroup only after the timeout.
        KillMode = "mixed";
        KillSignal = "SIGTERM";
        TimeoutStopSec = 90;
        RuntimeDirectory = "cryptomatord";
        Environment =
          "PATH=${lib.makeBinPath ([ cfg.cliPackage pkgs.fuse3 pkgs.coreutils pkgs.bash ] ++ cfg.extraPackages)}";
      };

      Install = {
        WantedBy = [ "graphical-session.target" ];
      };
    };
  };
}
