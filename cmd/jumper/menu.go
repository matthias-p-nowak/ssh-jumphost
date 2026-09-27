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
		hosts, err := readHosts()
		if err != nil {
			return err
		}
		printHosts(person, hosts)
		return nil
	}
	in := newTerminalInput()
	for {
		hosts, err := readHosts()
		if err != nil {
			return err
		}
		printHosts(person, hosts)
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
		if err := connect(in, hosts[n-1]); err != nil {
			fmt.Printf("  %v\n\n", err)
		}
	}
}

// printHosts prints the numbered host list with online state and the
// ready-to-copy ssh -J lines.
func printHosts(person string, hosts []*hostEntry) {
	fmt.Printf("\njumper - logged in as %s\n\n", person)
	if len(hosts) == 0 {
		fmt.Println("  no hosts configured")
	}
	for i, h := range hosts {
		state := "offline"
		if online(h) {
			state = "online"
		}
		fmt.Printf("  %d  %-12s port %d  %-7s  %s\n", i+1, h.Name, h.Port, state, h.Description)
		user := h.User
		if user == "" {
			user = "<user>"
		}
		fmt.Printf("       ssh -J jumper -p %d %s@localhost\n", h.Port, user)
		for _, lan := range h.LAN {
			fmt.Printf("     lan %s  %s\n", lan.Target, lan.Description)
			fmt.Printf("       ssh -J jumper,%s@localhost:%d <user>@%s\n", user, h.Port, lan.Target)
		}
	}
	fmt.Println()
	if os.Getenv("SSH_AUTH_SOCK") == "" {
		fmt.Println("  (connecting from here needs agent forwarding: log in with ssh -A)")
		fmt.Println()
	}
}

// online reports whether the tunnel of h is up, i.e. its port accepts
// connections on the jump host.
func online(h *hostEntry) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", h.Port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// connect opens an interactive ssh session to h through its tunnel,
// authenticating with the person's forwarded agent.
func connect(in *terminalInput, h *hostEntry) error {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return errors.New("connecting needs agent forwarding: log in with ssh -A")
	}
	if !online(h) {
		return fmt.Errorf("%s is offline (no tunnel on port %d)", h.Name, h.Port)
	}
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
		return err
	}
	defer client.Close()
	// Let the person hop further from the target with the same agent.
	if err := agent.ForwardToAgent(client, ag); err != nil {
		return err
	}
	return runSession(in, client)
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
