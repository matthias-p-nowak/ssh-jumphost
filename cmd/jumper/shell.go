package main

import (
	"errors"
	"fmt"
	"os/user"
	"strconv"
)

// runShell runs when sshd starts jumper as the login shell of an account
// (argv[0] "-jumper", or `jumper -c <command>` in args). Host accounts only
// get a hint; persons get the menu, commands are refused.
func runShell(args []string) error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	if u.Gid == strconv.Itoa(hostsGID) {
		fmt.Println("tunnel-only account: use ssh -N -R <port>:localhost:22 ...")
		return nil
	}
	if len(args) > 0 && args[0] == "-c" {
		return errors.New("commands are not allowed here; log in interactively (ssh -A) or use ssh -J")
	}
	return runMenu(u.Username)
}
