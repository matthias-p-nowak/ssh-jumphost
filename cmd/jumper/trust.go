package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// errKeyCaptured aborts the handshake once the host key has been seen.
var errKeyCaptured = errors.New("host key captured")

// runTrust implements `jumper trust [-y] <host>`: fetch the host key of a
// tunnel host, show its fingerprint, and after confirmation record it in
// the global known_hosts file (replacing an older entry for that port).
func runTrust(args []string) error {
	yes := len(args) > 0 && args[0] == "-y"
	if yes {
		args = args[1:]
	}
	if len(args) != 1 {
		return errors.New("usage: jumper trust [-y] <host>")
	}
	hosts, err := readHosts()
	if err != nil {
		return err
	}
	var h *hostEntry
	for _, candidate := range hosts {
		if candidate.Name == args[0] {
			h = candidate
		}
	}
	if h == nil {
		return fmt.Errorf("no host %q in %s", args[0], hostsFile)
	}

	addr := fmt.Sprintf("localhost:%d", h.Port)
	var key ssh.PublicKey
	config := &ssh.ClientConfig{
		User: "jumper-trust",
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			key = k
			return errKeyCaptured
		},
		Timeout: 10 * time.Second,
	}
	if _, err := ssh.Dial("tcp", addr, config); key == nil {
		return fmt.Errorf("cannot get the host key of %s via port %d (is the tunnel up?): %v", h.Name, h.Port, err)
	}
	fmt.Printf("%s via %s: %s %s\n", h.Name, addr, key.Type(), ssh.FingerprintSHA256(key))
	fmt.Printf("compare on %s: ssh-keygen -lf /etc/ssh/ssh_host_%s_key.pub\n", h.Name, hostKeyFileType(key.Type()))

	if !yes {
		if !isTerminal() {
			return errors.New("no terminal to confirm: check the fingerprint, then run with -y")
		}
		answer, err := ask("trust this key? (y/N)", "")
		if err != nil {
			return err
		}
		if answer != "y" && answer != "yes" {
			fmt.Println("not trusted")
			return nil
		}
	}

	pattern := knownhosts.Normalize(addr)
	if err := dropKnownHost(addr); err != nil {
		return err
	}
	if err := appendLine(at(knownHostsFile), knownhosts.Line([]string{pattern}, key), 0o644); err != nil {
		return err
	}
	fmt.Printf("trusted %s for %s\n", ssh.FingerprintSHA256(key), pattern)
	return nil
}

// hostKeyFileType returns the part of the OpenSSH host key file name
// (/etc/ssh/ssh_host_<part>_key.pub) for a key type, e.g. "ecdsa" for
// "ecdsa-sha2-nistp256".
func hostKeyFileType(keyType string) string {
	switch {
	case strings.HasPrefix(keyType, "ecdsa-"):
		return "ecdsa"
	case keyType == "ssh-dss":
		return "dsa"
	default: // ssh-ed25519, ssh-rsa
		return strings.TrimPrefix(keyType, "ssh-")
	}
}

// dropKnownHost removes the known_hosts lines for addr (host:port).
func dropKnownHost(addr string) error {
	pattern := knownhosts.Normalize(addr)
	lines, err := readLines(at(knownHostsFile))
	if err != nil {
		return err
	}
	kept := lines[:0]
	for _, line := range lines {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == pattern {
			continue
		}
		kept = append(kept, line)
	}
	return writeLines(at(knownHostsFile), kept, 0o644)
}
