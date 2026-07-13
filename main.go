// cryptomatord supervises cryptomator-cli vault mounts and exposes a control
// API over a unix socket. `serve` runs the daemon; `ctl` is its client.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "ctl":
		os.Exit(runCtl(os.Args[2:]))
	case "-h", "--help", "help":
		usage(os.Stdout)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "cryptomatord: unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	_, _ = fmt.Fprint(w, `cryptomatord — supervise cryptomator-cli vault mounts

usage:
  cryptomatord serve [--config <path>] [--log-level info]
  cryptomatord ctl [--socket <path>] status [<name>] [--json]
  cryptomatord ctl [--socket <path>] mount <name> [--json]
  cryptomatord ctl [--socket <path>] unmount <name> [--json]
`)
}
