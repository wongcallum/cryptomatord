package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/callum/cryptomatord/internal/client"
	"github.com/callum/cryptomatord/internal/config"
	"github.com/callum/cryptomatord/internal/state"
)

// ctlTimeout bounds a single ctl request (mounting can be slow).
const ctlTimeout = 120 * time.Second

// runCtl hand-parses args so flags may appear in any position (a widget or
// script invokes `ctl status --json`, with the flag after the subcommand).
func runCtl(args []string) int {
	socket := config.DefaultSocketPath()
	jsonOut := false
	var pos []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json" || a == "-json":
			jsonOut = true
		case a == "--socket" || a == "-socket":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "ctl: --socket requires a value")
				return 2
			}
			i++
			socket = args[i]
		case strings.HasPrefix(a, "--socket="):
			socket = strings.TrimPrefix(a, "--socket=")
		case a == "-h" || a == "--help":
			ctlUsage()
			return 0
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "ctl: unknown flag %q\n", a)
			return 2
		default:
			pos = append(pos, a)
		}
	}

	if len(pos) == 0 {
		ctlUsage()
		return 2
	}

	cl := client.New(socket)
	ctx, cancel := context.WithTimeout(context.Background(), ctlTimeout)
	defer cancel()

	switch pos[0] {
	case "status":
		if len(pos) >= 2 {
			st, err := cl.Status(ctx, pos[1])
			if err != nil {
				return ctlError(err)
			}
			printStatuses([]state.Status{st}, jsonOut)
			return 0
		}
		list, err := cl.List(ctx)
		if err != nil {
			return ctlError(err)
		}
		printStatuses(list, jsonOut)
		return 0

	case "mount", "unmount":
		if len(pos) < 2 {
			fmt.Fprintf(os.Stderr, "ctl %s: vault name required\n", pos[0])
			return 2
		}
		var st state.Status
		var err error
		if pos[0] == "mount" {
			st, err = cl.Mount(ctx, pos[1])
		} else {
			st, err = cl.Unmount(ctx, pos[1])
		}
		if err != nil {
			return ctlError(err)
		}
		printStatuses([]state.Status{st}, jsonOut)
		if st.State == state.Failed {
			return 1
		}
		return 0

	default:
		fmt.Fprintf(os.Stderr, "ctl: unknown subcommand %q\n", pos[0])
		ctlUsage()
		return 2
	}
}

func ctlUsage() {
	fmt.Fprint(os.Stderr, `usage:
  cryptomatord ctl [--socket <path>] status [<name>] [--json]
  cryptomatord ctl [--socket <path>] mount <name> [--json]
  cryptomatord ctl [--socket <path>] unmount <name> [--json]
`)
}

func ctlError(err error) int {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	return 1
}

// printStatuses prints a table, or single-line compact JSON when jsonOut is set
// (one document per line so a line-based parser sees it whole).
func printStatuses(sts []state.Status, jsonOut bool) {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(sts)
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tSTATE\tMOUNTPOINT\tINFO")
	for _, s := range sts {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, s.State, s.MountPoint, s.Error)
	}
	_ = tw.Flush()
}
