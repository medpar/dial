// Package procs finds running Claude and Codex processes with a single
// `ps` call, so dial can warn before swapping credentials under them.
package procs

import (
	"bufio"
	"bytes"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type Proc struct {
	PID  int
	Args string
}

func List() []Proc {
	out, err := exec.Command("ps", "-axww", "-o", "pid=,args=").Output()
	if err != nil {
		return nil
	}
	var ps []Proc
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:sp])
		if err != nil {
			continue
		}
		ps = append(ps, Proc{PID: pid, Args: strings.TrimSpace(line[sp+1:])})
	}
	return ps
}

var claudeVersionRe = regexp.MustCompile(`claude/versions/(\d+\.\d+\.\d+)`)

// ClaudeCLI is a running Claude Code CLI that reads the default credential
// store (desktop-embedded copies get a token through the environment).
type ClaudeCLI struct {
	PID     int
	Version string
}

func ClaudeCLIs(ps []Proc) []ClaudeCLI {
	var out []ClaudeCLI
	for _, p := range ps {
		a := p.Args
		if strings.Contains(a, "Claude.app/") || strings.Contains(a, "Application Support/Claude/") || strings.Contains(a, "--chrome-native-host") {
			continue
		}
		first := a
		if i := strings.IndexByte(a, ' '); i > 0 {
			first = a[:i]
		}
		if m := claudeVersionRe.FindStringSubmatch(first); m != nil {
			out = append(out, ClaudeCLI{PID: p.PID, Version: m[1]})
			continue
		}
		if strings.HasSuffix(first, "/claude") || first == "claude" {
			out = append(out, ClaudeCLI{PID: p.PID})
		}
	}
	return out
}

// CodexCLIs returns running codex CLI / app-server processes.
func CodexCLIs(ps []Proc) []Proc {
	var out []Proc
	for _, p := range ps {
		first := p.Args
		if i := strings.IndexByte(first, ' '); i > 0 {
			first = first[:i]
		}
		if strings.HasSuffix(first, "/codex") || first == "codex" {
			out = append(out, p)
		}
	}
	return out
}

// VersionLess compares dotted numeric versions.
func VersionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}
