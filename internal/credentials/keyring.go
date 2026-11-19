package credentials

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"time"

	gokeyring "github.com/zalando/go-keyring"
)

// ErrNotFound is returned when a secret is not there (in the keyring, or in the file).
var ErrNotFound = errors.New("secret not found")

// serviceName is the keyring service every secret is stored under.
const serviceName = "issue-timeline"

// Keyring is the OS keyring. Names are the full names ("redmine-api-key@https://host" included).
type Keyring interface {
	Get(name string) (string, error)
	Set(name, value string) error
	Delete(name string) error
}

// SystemKeyring is the keyring of this system: secret-tool (libsecret) on Linux, the same one the Node server used, so
// the secrets it saved are found; the Keychain on macOS and the Credential Manager on Windows.
func SystemKeyring() Keyring {
	if runtime.GOOS == "linux" {
		return secretTool{run: execRun}
	}
	return goKeyring{}
}

// runFunc runs a command with stdin, and returns its stdout. An error is any failure: not installed, a non-zero exit,
// or the timeout.
type runFunc func(ctx context.Context, name string, args []string, stdin string) (string, error)

// keyringTimeout bounds one secret-tool call: the first read of a credential can take several seconds.
const keyringTimeout = 15 * time.Second

func execRun(ctx context.Context, name string, args []string, stdin string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// secretTool is the keyring through secret-tool. A value is only ever on its stdin.
type secretTool struct {
	run runFunc
}

func (k secretTool) exec(stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), keyringTimeout)
	defer cancel()
	return k.run(ctx, "secret-tool", args, stdin)
}

// Get reads a secret. Any failure (secret-tool missing, no entry) is ErrNotFound, as the JS treats it: the file is
// tried next.
func (k secretTool) Get(name string) (string, error) {
	out, err := k.exec("", "lookup", "service", serviceName, "name", name)
	if err != nil {
		return "", ErrNotFound
	}
	// secret-tool ends the value with a newline, which is not part of it
	if strings.HasSuffix(out, "\n") {
		out = strings.TrimSuffix(strings.TrimSuffix(out, "\n"), "\r")
	}
	if out == "" {
		return "", ErrNotFound
	}
	return out, nil
}

// Set stores a secret; the value goes on stdin.
func (k secretTool) Set(name, value string) error {
	_, err := k.exec(value, "store", "--label", serviceName+" "+name, "service", serviceName, "name", name)
	return err
}

// Delete removes a secret. A failure is ErrNotFound: nothing was removed.
func (k secretTool) Delete(name string) error {
	if _, err := k.exec("", "clear", "service", serviceName, "name", name); err != nil {
		return ErrNotFound
	}
	return nil
}

// goKeyring is the keyring of macOS and Windows (and any other system go-keyring knows).
type goKeyring struct{}

func (goKeyring) Get(name string) (string, error) {
	v, err := gokeyring.Get(serviceName, name)
	if errors.Is(err, gokeyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func (goKeyring) Set(name, value string) error {
	return gokeyring.Set(serviceName, name, value)
}

func (goKeyring) Delete(name string) error {
	err := gokeyring.Delete(serviceName, name)
	if errors.Is(err, gokeyring.ErrNotFound) {
		return ErrNotFound
	}
	return err
}
