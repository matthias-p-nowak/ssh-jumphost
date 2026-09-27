package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// hostEntry is one `host` line of the hosts file plus its `lan` lines.
type hostEntry struct {
	Name        string     // host name, also the name of its host account
	Port        int        // fixed tunnel port on the jump host
	User        string     // default login user on the host for the menu; "" = none (`-`)
	Description string     // free text shown in the menu
	LAN         []lanEntry // targets reachable through this host
}

// lanEntry is one `lan` line: a target in the LAN behind a host.
type lanEntry struct {
	Target      string // address as seen from the host
	Description string // free text shown in the menu
}

// noUser in the default-user column of the hosts file means "no default user".
const noUser = "-"

// namePattern restricts account and host names to safe Unix user names.
var namePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// readHosts parses the hosts file (see docs/design.md "Hosts file").
// Errors name the offending line; names and ports must be unique.
func readHosts() ([]*hostEntry, error) {
	f, err := os.Open(at(hostsFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var hosts []*hostEntry
	byName := map[string]*hostEntry{}
	usedPorts := map[int]string{}
	scanner := bufio.NewScanner(f)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		fail := func(format string, args ...any) error {
			return fmt.Errorf("%s line %d: %s", hostsFile, lineNo, fmt.Sprintf(format, args...))
		}
		switch fields[0] {
		case "host":
			if len(fields) < 3 {
				return nil, fail("want: host <name> <port> [<default-user>|- [description]]")
			}
			name, user := fields[1], noUser
			if len(fields) > 3 {
				user = fields[3]
			}
			if !namePattern.MatchString(name) || (user != noUser && !namePattern.MatchString(user)) {
				return nil, fail("invalid name %q or user %q", name, user)
			}
			if user == noUser {
				user = ""
			}
			port, err := strconv.Atoi(fields[2])
			if err != nil || port < 1024 || port > 65535 {
				return nil, fail("port %q not in 1024-65535", fields[2])
			}
			if byName[name] != nil {
				return nil, fail("duplicate host %q", name)
			}
			if other, ok := usedPorts[port]; ok {
				return nil, fail("port %d already used by %q", port, other)
			}
			h := &hostEntry{Name: name, Port: port, User: user, Description: strings.Join(fields[4:], " ")}
			hosts = append(hosts, h)
			byName[name] = h
			usedPorts[port] = name
		case "lan":
			if len(fields) < 3 {
				return nil, fail("want: lan <host> <target> [description]")
			}
			h := byName[fields[1]]
			if h == nil {
				return nil, fail("unknown host %q (lan lines must follow their host line)", fields[1])
			}
			h.LAN = append(h.LAN, lanEntry{Target: fields[2], Description: strings.Join(fields[3:], " ")})
		default:
			return nil, fail("unknown keyword %q", fields[0])
		}
	}
	return hosts, scanner.Err()
}
