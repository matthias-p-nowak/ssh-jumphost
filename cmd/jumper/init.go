package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/crypto/ssh"
)

// Container paths used during startup (see docs/design.md "Volumes and startup").
const (
	skeletonDir       = "/usr/share/jumper/etc"
	etcDir            = "/etc"
	sshdConfigFile    = "/etc/ssh/sshd_config"
	sshdConfigDir     = "/etc/ssh/sshd_config.d"
	hostKeyFile       = "/etc/ssh/ssh_host_ed25519_key"
	hostsFile         = "/etc/jumper/hosts"
	authorizedKeysDir = "/etc/ssh/authorized_keys"
	knownHostsFile    = "/etc/ssh/ssh_known_hosts"
	sshdBinary        = "/usr/sbin/sshd"
)

// runInit prepares /etc and then replaces itself with sshd in the foreground.
// With -n it only prepares /etc and returns.
func runInit(args []string) error {
	prepareOnly := len(args) == 1 && args[0] == "-n"
	if len(args) > 0 && !prepareOnly {
		return errors.New("usage: jumper init [-n]")
	}

	// 1. First run: no sshd config in the /etc volume yet -> seed from the skeleton.
	if _, err := os.Stat(at(sshdConfigFile)); errors.Is(err, fs.ErrNotExist) {
		logf("seeding %s from %s", etcDir, skeletonDir)
		if err := copyTree(at(skeletonDir), at(etcDir)); err != nil {
			return fmt.Errorf("seeding %s: %w", etcDir, err)
		}
	}

	// 2. Host key, persisted in the volume.
	if err := ensureHostKey(at(hostKeyFile)); err != nil {
		return fmt.Errorf("host key: %w", err)
	}

	// 3. Jumper state files and directories.
	for _, dir := range []string{filepath.Dir(hostsFile), authorizedKeysDir, sshdConfigDir} {
		if err := os.MkdirAll(at(dir), 0o755); err != nil {
			return err
		}
	}
	for _, file := range []string{hostsFile, knownHostsFile} {
		if err := touch(at(file), 0o644); err != nil {
			return err
		}
	}

	// 4. Per-account sshd rules; sshd is not running yet, so no reload.
	// A broken hosts file must not keep sshd down: log and keep the old rules.
	if err := syncConfig(false); err != nil {
		logf("sync failed, starting with previous rules: %v", err)
	}

	if prepareOnly {
		return nil
	}

	// 5. Check the config, then become sshd (PID 1).
	sshd := at(sshdBinary)
	if out, err := exec.Command(sshd, "-t").CombinedOutput(); err != nil {
		return fmt.Errorf("sshd -t: %v\n%s", err, out)
	}
	return syscall.Exec(sshd, []string{sshd, "-D", "-e"}, os.Environ())
}

// copyTree copies the directory tree src into dst, keeping file modes.
// Files that already exist in dst are left untouched.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if _, err := os.Lstat(target); err == nil {
			return nil
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

// copyFile copies the regular file src to a new file dst with mode perm.
func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ensureHostKey creates an ed25519 host key pair at path and path.pub
// in OpenSSH format, unless the private key already exists.
func ensureHostKey(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	logf("creating host key %s", hostKeyFile)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "jumper host key")
	if err != nil {
		return err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return err
	}
	return os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(sshPub), 0o644)
}

// touch creates an empty file with mode perm if it does not exist.
func touch(path string, perm fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, perm)
	if err != nil {
		return err
	}
	return f.Close()
}

// logf prints a startup message to stderr (visible in `docker compose logs`).
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "jumper init: "+format+"\n", args...)
}
