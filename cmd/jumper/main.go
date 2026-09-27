// Command jumper is the only program of the SSH jump host image besides sshd:
// container entrypoint, account management and login shell.
// See docs/design.md "Components".
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// usage lists the implemented subcommands.
const usage = `usage:
  jumper init [-n]                  prepare /etc and start sshd (-n: prepare only)
  jumper add person <name>          create a person account
  jumper add host <name> [<port> [<default-user>|- [description]]]
                                    create a host account and its hosts-file line
                                    (missing values are asked with docker exec -it)
  jumper key <name>                 add public keys (piped on stdin, or pasted)
  jumper remove <name>              delete account, its keys and hosts-file lines
  jumper trust [-y] <host>          record a host's key for menu connect
  jumper sync                       regenerate sshd rules from the hosts file, reload sshd`

// root is the filesystem root all container paths are resolved against.
// It is "/" in the container; tests point JUMPER_ROOT at a scratch directory.
var root = rootDir()

// rootDir returns JUMPER_ROOT if set, otherwise "/".
func rootDir() string {
	if r := os.Getenv("JUMPER_ROOT"); r != "" {
		return r
	}
	return "/"
}

// at resolves an absolute container path against root.
func at(path string) string {
	return filepath.Join(root, path)
}

// main runs the subcommand, or the login shell when sshd started jumper
// as one (argv[0] "-jumper" or `jumper -c <command>`), and exits non-zero on error.
func main() {
	var err error
	if strings.HasPrefix(filepath.Base(os.Args[0]), "-") || (len(os.Args) > 1 && os.Args[1] == "-c") {
		err = runShell(os.Args[1:])
	} else {
		err = run(os.Args[1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "jumper:", err)
		os.Exit(1)
	}
}

// run dispatches on the subcommand name in args[0].
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing command\n%s", usage)
	}
	switch args[0] {
	case "init":
		return runInit(args[1:])
	case "add":
		return runAdd(args[1:])
	case "key":
		return runKey(args[1:])
	case "remove":
		return runRemove(args[1:])
	case "trust":
		return runTrust(args[1:])
	case "sync":
		return runSync(args[1:])
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}
