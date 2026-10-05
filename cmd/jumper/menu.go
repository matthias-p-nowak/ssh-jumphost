package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/term"
)

// runMenu shows the hosts to a person and connects on selection
// (see docs/design.md "Menu"). Without a terminal the list is printed once.
func runMenu(person string) error {
	if !isTerminal() {
		cfg, err := readHostsFile()
		if err != nil {
			return err
		}
		printHosts(person, cfg)
		return nil
	}
	in := newTerminalInput()
	// failedHost and failedErr describe the last failed connect: shown
	// below the list, and a retry of the same host runs with diagnostics.
	failedHost, failedErr := "", error(nil)
	for {
		cfg, err := readHostsFile()
		if err != nil {
			return err
		}
		hosts := cfg.Hosts
		printHosts(person, cfg)
		if failedErr != nil {
			fmt.Printf("  last connect to %s failed: %v\n", failedHost, failedErr)
			fmt.Printf("  (select %s again for diagnostics)\n\n", failedHost)
		}
		fmt.Print("number = connect, r = refresh, q = quit: ")
		line, err := in.ReadLine()
		if err != nil {
			fmt.Println()
			return nil
		}
		switch line = strings.TrimSpace(line); line {
		case "q", "quit", "exit":
			return nil
		case "", "r":
			continue
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(hosts) {
			fmt.Printf("  no host %q\n\n", line)
			continue
		}
		h := hosts[n-1]
		err = connect(in, h, failedErr != nil && failedHost == h.Name)
		failedHost, failedErr = h.Name, err
	}
}

// printHosts prints the numbered host list with online state and the
// ready-to-copy ssh -J lines: complete commands for person when the hosts
// file has a `public` line, otherwise with the `jumper` alias of the
// sample client config.
func printHosts(person string, cfg *hostsConfig) {
	fmt.Printf("\njumper - logged in as %s\n\n", person)
	if len(cfg.Hosts) == 0 {
		fmt.Println("  no hosts configured")
	}
	jump := "jumper"
	if cfg.PublicAddr != "" {
		jump = fmt.Sprintf("%s@%s:%d", person, cfg.PublicAddr, cfg.PublicPort)
	}
	for i, h := range cfg.Hosts {
		state := "offline"
		if online(h) {
			state = "online"
		}
		fmt.Printf("  %d  %-12s port %d  %-7s  %s\n", i+1, h.Name, h.Port, state, h.Description)
		user := h.User
		if user == "" {
			user = "<user>"
		}
		if cfg.PublicAddr != "" {
			fmt.Printf("       ssh -J %s -p %d -o HostKeyAlias=%s %s@localhost\n", jump, h.Port, h.Name, user)
		} else {
			fmt.Printf("       ssh -J %s -p %d %s@localhost\n", jump, h.Port, user)
		}
		for _, lan := range h.LAN {
			fmt.Printf("     lan %s  %s\n", lan.Target, lan.Description)
			fmt.Printf("       ssh -J %s,%s@localhost:%d <user>@%s\n", jump, user, h.Port, lan.Target)
		}
	}
	fmt.Println()
	if os.Getenv("SSH_AUTH_SOCK") == "" {
		fmt.Println("  (connecting from here needs agent forwarding: log in with ssh -A)")
		fmt.Println()
	}
}

// online reports whether the tunnel of h is up, i.e. sshd listens on its
// port on the jump host. It reads the kernel socket tables instead of
// connecting: a probe connection would go through the tunnel and count as
// an unauthenticated connection at the target (OpenSSH PerSourcePenalties).
func online(h *hostEntry) bool {
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if listening(table, h.Port) {
			return true
		}
	}
	return false
}

// tcpListen is the state code of a listening socket in /proc/net/tcp*.
const tcpListen = "0A"

// listening reports whether the socket table at path (/proc/net/tcp
// format: local_address "ADDR:PORT" in hex, state in hex) has a socket
// listening on port.
func listening(path string, port int) bool {
	lines, err := readLines(path)
	if err != nil {
		return false
	}
	for _, line := range lines[min(1, len(lines)):] { // skip the header
		f := strings.Fields(line)
		if len(f) < 4 || f[3] != tcpListen {
			continue
		}
		i := strings.LastIndexByte(f[1], ':')
		if p, err := strconv.ParseUint(f[1][i+1:], 16, 16); err == nil && int(p) == port {
			return true
		}
	}
	return false
}

// connect opens an interactive ssh session to h through its tunnel,
// authenticating with the person's forwarded agent. With verbose set,
// each step is explained, with a hint for the step that fails.
func connect(in *terminalInput, h *hostEntry, verbose bool) error {
	note := func(format string, args ...any) {
		if verbose {
			fmt.Printf("  - "+format+"\n", args...)
		}
	}
	if verbose {
		fmt.Printf("diagnostics for %s:\n", h.Name)
	}
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		note("no forwarded agent (SSH_AUTH_SOCK is empty).")
		note("on your client, `ssh-add -l` must list your key; if it reports no agent, run")
		note("`eval $(ssh-agent)` and `ssh-add`, then log in again with ssh -A")
		return errors.New("connecting needs agent forwarding: log in with ssh -A")
	}
	note("forwarded agent: %s", sock)
	if !online(h) {
		note("nothing listens on port %d: the tunnel of %s is down;", h.Port, h.Name)
		note("on %s check `systemctl status jumper-tunnel@%s`", h.Name, h.Name)
		return fmt.Errorf("%s is offline (no tunnel on port %d)", h.Name, h.Port)
	}
	note("tunnel port %d is listening", h.Port)
	user := h.User
	for user == "" {
		fmt.Printf("login user on %s: ", h.Name)
		line, err := in.ReadLine()
		if err != nil {
			return err
		}
		if line = strings.TrimSpace(line); namePattern.MatchString(line) {
			user = line
		} else if line != "" {
			fmt.Printf("  invalid user name %q\n", line)
		}
	}

	agentConn, err := net.Dial("unix", sock)
	if err != nil {
		return fmt.Errorf("forwarded agent: %w", err)
	}
	defer agentConn.Close()
	ag := agent.NewClient(agentConn)
	if verbose {
		keys, err := ag.List()
		switch {
		case err != nil:
			note("cannot list the agent's keys: %v", err)
		case len(keys) == 0:
			note("the agent holds no keys: run `ssh-add` on your client")
		default:
			note("the agent holds %d key(s):", len(keys))
			for _, k := range keys {
				note("  %s %s %s", k.Type(), ssh.FingerprintSHA256(k), k.Comment)
			}
		}
	}

	known, err := knownhosts.New(at(knownHostsFile))
	if err != nil {
		return err
	}
	config := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeysCallback(ag.Signers)},
		HostKeyCallback: hostKeyCheck(h, known),
		Timeout:         10 * time.Second,
	}
	fmt.Printf("connecting to %s@%s ...\n", user, h.Name)
	client, err := ssh.Dial("tcp", fmt.Sprintf("localhost:%d", h.Port), config)
	if err != nil {
		if verbose {
			explainDialError(note, h, user, err)
		}
		return err
	}
	note("connected, %s runs %s", h.Name, client.ServerVersion())
	defer client.Close()
	// Let the person hop further from the target with the same agent.
	if err := agent.ForwardToAgent(client, ag); err != nil {
		return err
	}
	return runSession(in, client)
}

// explainDialError prints, via note, the likely cause of a failed ssh.Dial
// to h and what to check.
func explainDialError(note func(string, ...any), h *hostEntry, user string, err error) {
	// x/crypto reports every handshake error as "ssh: handshake failed: ...",
	// so the specific cases are recognised by their text first.
	msg := err.Error()
	switch {
	case strings.Contains(msg, "host key of "): // from hostKeyCheck
		note("the host key check failed (see the message below); the admin compares the")
		note("fingerprint on %s and runs `jumper trust %s`", h.Name, h.Name)
	case strings.Contains(msg, "unable to authenticate"):
		note("%s rejected every key of your agent for user %s:", h.Name, user)
		note("one of them must be in ~%s/.ssh/authorized_keys on %s", user, h.Name)
	case strings.Contains(msg, "handshake failed"):
		note("the tunnel is up, but the far end closed the connection before the ssh handshake.")
		note("on %s check that sshd listens where the tunnel points (-R %d:localhost:<sshd port>);", h.Name, h.Port)
		note("its log may show `srclimit_penalise` (PerSourcePenalties blocking ::1)")
	case strings.Contains(msg, "timeout"):
		note("no answer within 10 s: the tunnel or %s hangs", h.Name)
	default:
		note("unexpected error")
	}
}

// hostKeyCheck wraps the known_hosts check with messages that tell the
// person what to do about an unknown or changed key.
func hostKeyCheck(h *hostEntry, known ssh.HostKeyCallback) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := known(hostname, remote, key)
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) {
			if len(keyErr.Want) == 0 {
				return fmt.Errorf("host key of %s is unknown (%s); ask the admin to run `jumper trust %s`",
					h.Name, ssh.FingerprintSHA256(key), h.Name)
			}
			return fmt.Errorf("host key of %s has CHANGED (%s), refusing to connect; the admin must check it and run `jumper trust %s`",
				h.Name, ssh.FingerprintSHA256(key), h.Name)
		}
		return err
	}
}

// runSession runs an interactive shell on client with the local terminal
// in raw mode, forwarding window-size changes, until the remote side ends.
func runSession(in *terminalInput, client *ssh.Client) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	_ = agent.RequestAgentForwarding(session)

	fd := int(os.Stdin.Fd())
	width, height, err := term.GetSize(fd)
	if err != nil {
		width, height = 80, 24
	}
	termName := os.Getenv("TERM")
	if termName == "" {
		termName = "xterm"
	}
	if err := session.RequestPty(termName, height, width, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		return err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	state, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, state)

	// Stdin is copied by our own goroutine (not session.Stdin), so the
	// session can end without waiting for the next keystroke.
	done := make(chan struct{})
	defer close(done)
	go func() {
		_, _ = io.Copy(stdin, sessionReader{in: in, done: done})
		stdin.Close()
	}()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for {
			select {
			case <-winch:
				if w, h, err := term.GetSize(fd); err == nil {
					_ = session.WindowChange(h, w)
				}
			case <-done:
				return
			}
		}
	}()

	if err := session.Shell(); err != nil {
		return err
	}
	err = session.Wait()
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
