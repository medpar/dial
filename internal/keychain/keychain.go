// Package keychain reads and writes macOS generic passwords through
// /usr/bin/security, the same tool Claude Code uses, so items keep an ACL
// that both programs can read without prompting. Secrets are passed on
// stdin (security -i), never on the command line.
package keychain

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

var ErrNotFound = errors.New("keychain item not found")

const securityBin = "/usr/bin/security"

// Available reports whether the macOS keychain can be used.
func Available() bool { return runtime.GOOS == "darwin" }

// Get returns the password data of the generic password (service, account).
// An empty account matches any account.
func Get(service, account string) ([]byte, error) {
	args := []string{"find-generic-password", "-s", service}
	if account != "" {
		args = append(args, "-a", account)
	}
	args = append(args, "-w")
	cmd := exec.Command(securityBin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if strings.Contains(errb.String(), "could not be found") {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("security find-generic-password %q: %s", service, strings.TrimSpace(errb.String()))
	}
	data := bytes.TrimRight(out.Bytes(), "\n")
	// security prints non-printable payloads as hex; decode those back.
	if len(data) > 0 && data[0] != '{' && isHex(data) {
		if dec, err := hex.DecodeString(string(data)); err == nil {
			return dec, nil
		}
	}
	return data, nil
}

// Set creates or updates (service, account) with data. Small payloads go
// through `security -i` on stdin; security's interactive line limit is 4 KiB,
// so larger ones fall back to argv (as Claude Code itself does). The write is
// read back because security can exit 0 without storing anything.
func Set(service, account string, data []byte) error {
	hexData := hex.EncodeToString(data)
	line := fmt.Sprintf("add-generic-password -U -a %s -s %s -X %s\n", quote(account), quote(service), hexData)
	var cmd *exec.Cmd
	if len(line) < 4000 {
		cmd = exec.Command(securityBin, "-i")
		cmd.Stdin = strings.NewReader(line)
	} else {
		cmd = exec.Command(securityBin, "add-generic-password", "-U", "-a", account, "-s", service, "-X", hexData)
	}
	// stderr can echo the hex payload; never surface it verbatim.
	var errb bytes.Buffer
	cmd.Stderr, cmd.Stdout = &errb, &errb
	runErr := cmd.Run()
	got, err := Get(service, account)
	if err == nil && bytes.Equal(got, data) {
		return nil
	}
	code := ""
	if ee, ok := runErr.(*exec.ExitError); ok {
		code = fmt.Sprintf(" (exit %d)", ee.ExitCode())
	}
	return fmt.Errorf("keychain write to %q did not persist%s; unlock the login keychain and retry", service, code)
}

// Delete removes (service, account). Missing items are not an error.
func Delete(service, account string) error {
	args := []string{"delete-generic-password", "-s", service}
	if account != "" {
		args = append(args, "-a", account)
	}
	cmd := exec.Command(securityBin, args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil && !strings.Contains(errb.String(), "could not be found") {
		return fmt.Errorf("security delete-generic-password %q: %s", service, strings.TrimSpace(errb.String()))
	}
	return nil
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func isHex(b []byte) bool {
	if len(b)%2 != 0 {
		return false
	}
	for _, c := range b {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
