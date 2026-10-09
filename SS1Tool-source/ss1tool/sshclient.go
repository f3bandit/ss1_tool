package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	sshMu     sync.Mutex
	sshClient *ssh.Client
	sshHost   string
	sshPass   string
)

var errNotConnected = errors.New("not connected - connect to the SuperStation first")

// shq quotes a string for safe use in a remote POSIX shell command.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func dial(host, user, pass string) (*ssh.Client, error) {
	if !strings.Contains(host, ":") {
		host += ":22"
	}
	conf := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(pass),
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				a := make([]string, len(qs))
				for i := range a {
					a[i] = pass
				}
				return a, nil
			}),
		},
		// MiSTer regenerates host keys on reflash; this is a LAN device tool.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         8 * time.Second,
	}
	return ssh.Dial("tcp", host, conf)
}

func connect(host, user, pass string) error {
	c, err := dial(host, user, pass)
	if err != nil {
		return err
	}
	sshMu.Lock()
	if sshClient != nil {
		_ = sshClient.Close()
	}
	sshClient, sshHost, sshPass = c, host, pass
	sshMu.Unlock()
	return nil
}

func disconnect() {
	sshMu.Lock()
	if sshClient != nil {
		_ = sshClient.Close()
	}
	sshClient = nil
	sshMu.Unlock()
}

// session opens a session, reconnecting once if the connection dropped (e.g. after a reboot).
// Only one goroutine reconnects at a time; the others use the connection it made.
var reconnMu sync.Mutex

func session() (*ssh.Session, error) {
	sshMu.Lock()
	c := sshClient
	sshMu.Unlock()
	if c == nil {
		return nil, errNotConnected
	}
	s, err := c.NewSession()
	if err == nil {
		return s, nil
	}
	reconnMu.Lock()
	defer reconnMu.Unlock()
	sshMu.Lock()
	cur, host, pass := sshClient, sshHost, sshPass
	sshMu.Unlock()
	if cur == nil {
		return nil, errNotConnected // disconnected meanwhile
	}
	if cur != c { // another request already reconnected
		return cur.NewSession()
	}
	cfgMu.Lock()
	user := cfg.User
	cfgMu.Unlock()
	if err := connect(host, user, pass); err != nil {
		return nil, fmt.Errorf("connection lost: %w", err)
	}
	sshMu.Lock()
	c = sshClient
	sshMu.Unlock()
	if c == nil {
		return nil, errNotConnected
	}
	return c.NewSession()
}

// run executes a command and returns combined stdout+stderr.
func run(cmd string) (string, error) {
	return runIn(cmd, nil)
}

// runIn executes a command with optional stdin data.
func runIn(cmd string, stdin io.Reader) (string, error) {
	s, err := session()
	if err != nil {
		return "", err
	}
	defer s.Close()
	// one locked writer for both streams (a shared bytes.Buffer loses data
	// because io.Copy uses its ReadFrom concurrently)
	out := &syncBuf{}
	s.Stdout = out
	s.Stderr = out
	if stdin != nil {
		s.Stdin = stdin
	}
	err = s.Run(cmd)
	return out.String(), err
}

// runTo streams a command's stdout into w (used for file downloads).
func runTo(cmd string, w io.Writer) error {
	s, err := session()
	if err != nil {
		return err
	}
	defer s.Close()
	var stderr bytes.Buffer
	s.Stdout = w
	s.Stderr = &stderr
	if err := s.Run(cmd); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// upload writes data to a remote path atomically (temp file + rename).
func upload(path string, data io.Reader, mode string) error {
	tmp := path + ".ss1tool.tmp"
	cmd := fmt.Sprintf("cat > %s && chmod %s %s && mv -f %s %s && sync",
		shq(tmp), mode, shq(tmp), shq(tmp), shq(path))
	out, err := runIn(cmd, data)
	if err != nil {
		_, _ = run("rm -f " + shq(tmp))
		return fmt.Errorf("upload %s failed: %v %s", path, err, strings.TrimSpace(out))
	}
	return nil
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuf) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuf) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func connected() bool {
	sshMu.Lock()
	defer sshMu.Unlock()
	return sshClient != nil
}

func isDisconnectErr(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var ee *ssh.ExitMissingError
	return errors.As(err, &ee) || errors.Is(err, io.EOF)
}
