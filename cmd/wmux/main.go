// Command wmux is the client half of wmux: it drives wmuxd (create
// sessions, list state, push agent notifications) and hosts the
// full-screen multiplexer TUI.
//
// Everything it does lives in internal/cli — this file exists only to
// name the binary, so that the CLI, the TUI (internal/tui), and the
// layout engine (internal/layout) are ordinary testable packages rather
// than one 36-file package main.
package main

import "github.com/peterkure3/wmux/internal/cli"

func main() { cli.Main() }
