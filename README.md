# cryptomatord

> [!WARNING]  
> This project, including the documentation below,
> was written with Claude Code. The output has not been verified by a human.
> Until it has been rewritten from scratch by a human (me), it remains
> strictly for my personal use. That being said, it does not contain any
> cryptographic code and only shells out to the official Cryptomator Java CLI.

A small Go daemon that sits between the Cryptomator GUI and the Cryptomator CLI:
it supervises one [`cryptomator-cli`](https://github.com/cryptomator/cli) process
per vault, brokers each vault's passphrase from a command you choose, and exposes
a control API over a unix socket. It's built to be driven headlessly and from
scripts or a desktop-shell widget.

It does **not** reimplement Cryptomator's cryptography or mounting — those stay in
the upstream, audited Java `cryptomator-cli`. cryptomatord only adds supervision,
control, and credential brokering.

## Why

Upstream ships a tray GUI that keeps many vaults mounted, and a CLI that unlocks
exactly one vault in the foreground (unmount via SIGINT) with no daemon, IPC, or
multi-vault support. cryptomatord fills that gap without a JVM-heavy GUI.

## Architecture

```
 widget/script ──exec──▶ cryptomatord ctl ──HTTP/unix socket──▶ cryptomatord serve
                                                                    │
                                              one supervised child per vault
                                                                    ▼
                                        cryptomator-cli unlock … (FUSE mount)
```

- **One binary, two subcommands:** `cryptomatord serve` (daemon) and
  `cryptomatord ctl …` (client).
- **Per-vault state machine:** `unmounted → unlocking → mounted`, with
  `failed`/`restarting`. A child that dies unexpectedly is restarted with capped
  exponential backoff. On daemon shutdown every vault is unmounted cleanly
  (SIGINT, then `fusermount3 -u` fallback).
- **Credentials:** each vault names a `passwordCommand`; its stdout is piped to
  `cryptomator-cli --password:stdin`. Nothing is stored by the daemon.
- **Control API:** HTTP over `$XDG_RUNTIME_DIR/cryptomatord/control.sock` (0600).

## Develop

```sh
nix develop            # Go toolchain, gopls, golangci-lint, cryptomator-cli, fuse3
go build ./...
go test -race ./...
```

Nix:

```sh
nix build .#cryptomatord                 # build the daemon
nix flake check                          # go tests + NixOS VM test (needs KVM)
nix run .#cryptomatord -- serve --config ./config.json
```

## Configuration

JSON. By default `serve` reads `$XDG_CONFIG_HOME/cryptomatord/config.json` (i.e.
`~/.config/cryptomatord/config.json`); pass `--config <path>` to override. The
home-manager module generates this config in the Nix store and passes it with
`--config`, so with home-manager you never write this file. With the NixOS
module or a non-Nix setup you write it yourself at the default XDG path:

```json
{
  "cliPath": "cryptomator-cli",
  "defaultMounter": "org.cryptomator.frontend.fuse.mount.LinuxFuseMountProvider",
  "vaults": {
    "work": {
      "path": "/home/alice/Cloud/work",
      "mountPoint": "/home/alice/mnt/work",
      "passwordCommand": "pass show vaults/work",
      "autoMount": true,
      "mounter": null,
      "mountOptions": []
    }
  }
}
```

- `socket` is optional; when omitted the daemon uses
  `$XDG_RUNTIME_DIR/cryptomatord/control.sock` (which `ctl` also defaults to).
- `autoMount` defaults to `true`.
- `mounter` defaults to `defaultMounter`; list options with
  `cryptomator-cli list-mounters`.

## ctl

```sh
cryptomatord ctl status              # table of all vaults
cryptomatord ctl status --json       # one-line JSON (for a widget or script)
cryptomatord ctl status work
cryptomatord ctl mount work          # blocks until mounted; exits 1 if it failed
cryptomatord ctl unmount work
cryptomatord ctl --socket /path/to.sock status
```

## Run it as a systemd user service (home-manager)

The home-manager module generates the config in the Nix store, passes it
straight to the daemon with `--config`, and runs it as a **systemd user
service**, so FUSE mounts land in your session and `passwordCommand` can reach
your `gpg-agent`/keyring. This is the self-contained way to run it:

```nix
{
  inputs.cryptomatord.url = "github:you/cryptomatord";

  # in your home-manager configuration:
  imports = [ inputs.cryptomatord.homeModules.default ];

  services.cryptomatord = {
    enable = true;
    extraPackages = [ pkgs.pass pkgs.gnupg ]; # tools your passwordCommand needs
    vaults.work = {
      path = "/home/alice/Cloud/work";
      mountPoint = "/home/alice/mnt/work";
      passwordCommand = "pass show vaults/work";
    };
  };
}
```

It uses `pkgs.cryptomator-cli` by default (override with
`services.cryptomatord.cliPackage`).

### NixOS module

The NixOS module runs the same systemd user service but **provides no config**:
the daemon reads `~/.config/cryptomatord/config.json`, which you supply with the
home-manager module above or by hand (schema in [Configuration](#configuration)).
Use it if you prefer to manage the service from your system configuration:

```nix
{
  inputs.cryptomatord.url = "github:you/cryptomatord";

  # in your NixOS configuration:
  imports = [ inputs.cryptomatord.nixosModules.default ];

  services.cryptomatord = {
    enable = true;
    extraPackages = [ pkgs.pass pkgs.gnupg ]; # tools your passwordCommand needs
  };
}
```

Enable the home-manager service or the NixOS one, not both.

## Widget / script integration

A widget or script shells out to `ctl` and parses JSON — no HTTP or socket code
needed. Run `cryptomatord ctl status --json` to read state, and
`cryptomatord ctl mount work` / `unmount work` for actions. Poll `status` on a
timer (or on button actions) to keep a widget live.

## Manual smoke test with a real vault

`cryptomator-cli` can only *unlock* existing vaults, so create one once with the
Cryptomator GUI, then:

```sh
nix develop
cryptomator-cli list-mounters                     # confirm your mounter class
cat > /tmp/cmd.json <<EOF
{ "cliPath": "$(command -v cryptomator-cli)",
  "vaults": { "test": {
    "path": "/path/to/vault", "mountPoint": "/tmp/mnt",
    "passwordCommand": "cat /path/to/passfile", "autoMount": false } } }
EOF
cryptomatord serve --config /tmp/cmd.json &
cryptomatord ctl mount test
ls /tmp/mnt          # cleartext
cryptomatord ctl unmount test
kill %1              # clean shutdown unmounts everything
```

## Limitations

- Linux only (FUSE + systemd).
- The "mounted" check assumes a real mount point (the default FUSE mounter);
  non-FUSE mounters (WebDAV) aren't a v1 target.
- Auto-lock policies (idle/suspend) and an event stream are future work.

## Security notes

- The daemon stores no secrets; `passwordCommand` output is written to the
  child's stdin and then zeroed.
- The control socket is per-user and mode `0600`.
