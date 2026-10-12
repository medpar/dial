// Package remote runs dial on another machine over the system ssh client,
// so ~/.ssh/config, agents and Tailscale names all just work.
package remote

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/medpar/dial/internal/state"
)

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '@' || r == '=' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Command builds the remote shell command for dial args.
func Command(r state.Remote, args []string) string {
	bin := `"$HOME/.local/bin/dial"`
	if r.Dial != "" {
		bin = shellQuote(r.Dial)
		if strings.HasPrefix(r.Dial, "~/") {
			bin = `"$HOME/"` + shellQuote(r.Dial[2:])
		}
	}
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	return bin + " " + strings.Join(q, " ")
}

// Run executes dial on r with stdin and returns stdout.
func Run(ctx context.Context, r state.Remote, args []string, stdin []byte, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=6",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=2",
		"-o", "LogLevel=ERROR",
		r.SSH, Command(r, args))
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if ctx.Err() == context.DeadlineExceeded {
			msg = "timed out"
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if out.Len() > 0 {
			// The agent reports structured errors on stdout; let the caller parse them.
			return out.Bytes(), fmt.Errorf("%s: %s", r.Name, firstLine(msg, err))
		}
		return nil, fmt.Errorf("%s: %s", r.Name, firstLine(msg, err))
	}
	return out.Bytes(), nil
}

func firstLine(msg string, err error) string {
	if msg == "" {
		return err.Error()
	}
	if i := strings.IndexByte(msg, '\n'); i > 0 {
		return msg[:i]
	}
	return msg
}
