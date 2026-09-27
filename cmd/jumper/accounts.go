package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Account database files and fixed ids (see docs/design.md "Accounts").
const (
	passwdFile = "/etc/passwd"
	shadowFile = "/etc/shadow"
	jumperPath = "/usr/local/bin/jumper"
	hostsGID   = 1001 // group `hosts`
	personsGID = 1002 // group `persons`
	firstUID   = 2000 // accounts managed by jumper start here
)

// account is the part of a passwd line jumper needs.
type account struct {
	Name string
	UID  int
	GID  int
}

// runAdd implements `jumper add host|person <name> [...]`: create the
// account (and for a host its hosts-file line), then sync. Keys are added
// separately with `jumper key`.
func runAdd(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: jumper add person <name> | jumper add host <name> [<port> [<default-user>|- [description]]]")
	}
	kind, name, rest := args[0], args[1], args[2:]
	gid, ok := map[string]int{"host": hostsGID, "person": personsGID}[kind]
	if !ok {
		return fmt.Errorf("unknown account type %q, want host or person", kind)
	}
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid name %q (lower case letters, digits, _ and -)", name)
	}
	if kind == "person" && len(rest) > 0 {
		return errors.New("usage: jumper add person <name>")
	}

	accounts, err := readAccounts()
	if err != nil {
		return err
	}
	acc := findAccount(accounts, name)
	if acc != nil && acc.GID != gid {
		return fmt.Errorf("%s exists with another account type", name)
	}
	if kind == "host" {
		if err := ensureHostLine(name, rest); err != nil {
			return err
		}
	}
	if acc == nil {
		if err := createAccount(name, nextUID(accounts), gid, kind); err != nil {
			return err
		}
		fmt.Printf("created %s account %s\n", kind, name)
	} else {
		fmt.Printf("%s account %s already exists\n", kind, name)
	}
	if err := syncConfig(true); err != nil {
		return err
	}
	fmt.Printf("next: add its public key with\n  docker exec -i <container> jumper key %s < %s.pub\n", name, name)
	return nil
}

// ensureHostLine makes sure the hosts file has a `host <name>` line. Values
// not given in args (port, default user, description) are asked on the
// terminal; without a terminal they are an error.
func ensureHostLine(name string, args []string) error {
	hosts, err := readHosts()
	if err != nil {
		return err
	}
	used := map[int]bool{}
	for _, h := range hosts {
		if h.Name == name {
			fmt.Printf("hosts file: %s already on port %d\n", name, h.Port)
			return nil
		}
		used[h.Port] = true
	}
	checkPort := func(s string) error {
		port, err := strconv.Atoi(s)
		if err != nil || port < 1024 || port > 65535 {
			return fmt.Errorf("port %q not in 1024-65535", s)
		}
		if used[port] {
			return fmt.Errorf("port %d is already used", port)
		}
		return nil
	}
	checkUser := func(s string) error {
		if s != noUser && !namePattern.MatchString(s) {
			return fmt.Errorf("invalid user name %q", s)
		}
		return nil
	}

	port, user, description := "", noUser, ""
	if len(args) >= 1 {
		port = args[0]
		if len(args) >= 2 {
			user, description = args[1], strings.Join(args[2:], " ")
		}
		if err := checkPort(port); err != nil {
			return err
		}
		if err := checkUser(user); err != nil {
			return err
		}
	} else {
		if !isTerminal() {
			return fmt.Errorf("%s is not in %s: give <port> [<default-user>|- [description]], or run with `docker exec -it` to be asked", name, hostsFile)
		}
		fmt.Printf("%s is not in %s yet.\n", name, hostsFile)
		free := 22001
		for used[free] {
			free++
		}
		if port, err = askValid("tunnel port", strconv.Itoa(free), checkPort); err != nil {
			return err
		}
		if user, err = askValid("default login user on "+name+" (empty: none)", noUser, checkUser); err != nil {
			return err
		}
		if description, err = ask("description", ""); err != nil {
			return err
		}
	}

	line := strings.TrimSpace(fmt.Sprintf("host %s %s %s %s", name, port, user, description))
	if err := appendRaw(at(hostsFile), line); err != nil {
		return err
	}
	fmt.Printf("hosts file: added %q\n", line)
	return nil
}

// runKey implements `jumper key <name>`: append public keys (piped on stdin,
// or pasted on the terminal) to the key file of an existing account.
func runKey(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: jumper key <name>  (public keys on stdin, or paste them)")
	}
	name := args[0]
	accounts, err := readAccounts()
	if err != nil {
		return err
	}
	acc := findAccount(accounts, name)
	if acc == nil || acc.UID < firstUID {
		return fmt.Errorf("no jumper account %q; create it with `jumper add` first", name)
	}

	var input string
	if isTerminal() {
		fmt.Println("Paste public key(s), one per line; finish with an empty line:")
		for {
			line, err := stdinReader.ReadString('\n')
			if strings.TrimSpace(line) == "" || err != nil {
				input += line
				break
			}
			input += line
		}
	} else {
		data, err := io.ReadAll(stdinReader)
		if err != nil {
			return err
		}
		input = string(data)
	}
	keys, err := parsePublicKeys(input)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("no public key given")
	}
	added, err := appendKeys(name, keys)
	if err != nil {
		return err
	}
	fmt.Printf("%d new key(s) for %s\n", added, name)
	if acc.GID == hostsGID {
		if hosts, err := readHosts(); err == nil {
			for _, h := range hosts {
				if h.Name == name {
					fmt.Printf("on %s: ssh -N -R %d:localhost:22 %s@<jumper>\n", name, h.Port, name)
				}
			}
		}
	}
	return nil
}

// runRemove implements `jumper remove <name>`: delete a jumper-managed
// account, its key file and, for hosts, its hosts-file lines and its
// known_hosts entry, then sync.
func runRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: jumper remove <name>")
	}
	name := args[0]
	accounts, err := readAccounts()
	if err != nil {
		return err
	}
	acc := findAccount(accounts, name)
	if acc == nil {
		return fmt.Errorf("no account %q", name)
	}
	if acc.UID < firstUID {
		return fmt.Errorf("%s is a system account, not managed by jumper", name)
	}
	for _, file := range []struct {
		path string
		perm fs.FileMode
	}{{passwdFile, 0o644}, {shadowFile, 0o600}} {
		if err := removeLine(file.path, name, file.perm); err != nil {
			return err
		}
	}
	if err := os.Remove(keyFile(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if acc.GID == hostsGID {
		if err := forgetHostKey(name); err != nil {
			return err
		}
		if err := removeHostLines(name); err != nil {
			return err
		}
	}
	fmt.Printf("removed %s\n", name)
	return syncConfig(true)
}

// parsePublicKeys validates authorized_keys lines; blank lines and
// comments are skipped.
func parsePublicKeys(input string) ([]string, error) {
	var keys []string
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err != nil {
			return nil, fmt.Errorf("invalid public key %.40q: %w", line, err)
		}
		keys = append(keys, line)
	}
	return keys, nil
}

// readAccounts parses /etc/passwd.
func readAccounts() ([]account, error) {
	lines, err := readLines(at(passwdFile))
	if err != nil {
		return nil, err
	}
	var accounts []account
	for _, line := range lines {
		f := strings.Split(line, ":")
		if len(f) < 4 {
			continue
		}
		uid, err1 := strconv.Atoi(f[2])
		gid, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil {
			continue
		}
		accounts = append(accounts, account{Name: f[0], UID: uid, GID: gid})
	}
	return accounts, nil
}

// findAccount returns the account called name, or nil.
func findAccount(accounts []account, name string) *account {
	for i := range accounts {
		if accounts[i].Name == name {
			return &accounts[i]
		}
	}
	return nil
}

// nextUID returns the next free uid at or above firstUID.
func nextUID(accounts []account) int {
	uid := firstUID
	for _, a := range accounts {
		if a.UID >= uid {
			uid = a.UID + 1
		}
	}
	return uid
}

// createAccount appends the passwd and shadow lines and an empty key file.
func createAccount(name string, uid, gid int, kind string) error {
	passwdLine := fmt.Sprintf("%s:x:%d:%d:jumper %s:/var/empty:%s", name, uid, gid, kind, jumperPath)
	if err := appendLine(at(passwdFile), passwdLine, 0o644); err != nil {
		return err
	}
	// "*" = no password, but not locked, so key login works.
	if err := appendLine(at(shadowFile), name+":*::0:::::", 0o600); err != nil {
		return err
	}
	_, err := appendKeys(name, nil)
	return err
}

// keyFile returns the host path of the authorized_keys file of name.
func keyFile(name string) string {
	return at(filepath.Join(authorizedKeysDir, name))
}

// appendKeys adds keys that are not yet in the key file of name and
// returns how many were added. The file is created if missing.
func appendKeys(name string, keys []string) (int, error) {
	path := keyFile(name)
	existing, err := readLines(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	present := map[string]bool{}
	for _, line := range existing {
		present[keyBlob(line)] = true
	}
	added := 0
	for _, key := range keys {
		if blob := keyBlob(key); !present[blob] {
			existing = append(existing, key)
			present[blob] = true
			added++
		}
	}
	return added, writeLines(path, existing, 0o644)
}

// keyBlob returns the base64 key part of an authorized_keys line, used to
// detect duplicates regardless of comments.
func keyBlob(line string) string {
	if pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err == nil {
		return string(pub.Marshal())
	}
	return line
}

// readLines returns the non-empty lines of a file.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if line := scanner.Text(); strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}

// writeLines writes lines to path atomically with mode perm.
func writeLines(path string, lines []string, perm fs.FileMode) error {
	data := strings.Join(lines, "\n")
	if data != "" {
		data += "\n"
	}
	return writeFileAtomic(path, []byte(data), perm)
}

// appendLine adds one line to path, keeping all existing lines.
func appendLine(path, line string, perm fs.FileMode) error {
	lines, err := readLines(path)
	if err != nil {
		return err
	}
	return writeLines(path, append(lines, line), perm)
}

// removeLine drops the entry of name (`name:...`) from a colon-separated
// account file.
func removeLine(path, name string, perm fs.FileMode) error {
	lines, err := readLines(at(path))
	if err != nil {
		return err
	}
	kept := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(line, name+":") {
			kept = append(kept, line)
		}
	}
	return writeLines(at(path), kept, perm)
}

// appendRaw appends one line to a text file, keeping its formatting
// (comments, blank lines) untouched.
func appendRaw(path, line string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := string(data)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return writeFileAtomic(path, []byte(text+line+"\n"), 0o644)
}

// removeHostLines drops the `host <name>` and `lan <name>` lines from the
// hosts file, keeping everything else as it is.
func removeHostLines(name string) error {
	data, err := os.ReadFile(at(hostsFile))
	if err != nil {
		return err
	}
	var kept []string
	for _, line := range strings.SplitAfter(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && (f[0] == "host" || f[0] == "lan") && f[1] == name {
			continue
		}
		kept = append(kept, line)
	}
	return writeFileAtomic(at(hostsFile), []byte(strings.Join(kept, "")), 0o644)
}

// forgetHostKey drops the known_hosts entry of host name's tunnel port, so a
// later host on the same port does not look like a changed key.
func forgetHostKey(name string) error {
	hosts, err := readHosts()
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if h.Name == name {
			return dropKnownHost(fmt.Sprintf("localhost:%d", h.Port))
		}
	}
	return nil
}
