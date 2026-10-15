// dial switches Claude and Codex accounts across CLIs, desktop apps and
// remote machines, and shows every account's usage limits.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/medpar/dial/internal/ui"
	"github.com/medpar/dial/internal/vault"
)

var version = "0.1.0"

const usageText = `dial — switch Claude & Codex accounts, see every limit

USAGE
  dial                          status of every account (cached usage)
  dial status [--refresh]       … refreshing stale usage first
  dial use <account>            switch CLI + desktop app (+ remotes)
  dial claude|codex <account>   shorthand for use
  dial next claude|codex        switch to the next account
  dial best claude|codex        switch to the account with most headroom
  dial add claude|codex         log in a new account (current login untouched)
  dial login <account>          renew a profile's login in the browser
  dial import [live|codex-auth|cps|all]
  dial ls                       list accounts
  dial rename <account> <label>
  dial rm <account>

SIDE BY SIDE
  dial run <account> [-- args]  run claude/codex as <account> in this terminal
  dial env <account>            print exports for a shell (eval "$(dial env x)")
  dial open <account>           open a desktop window signed in as <account>
  dial show <account>           bring an account's desktop window to the front
  dial dock [--pin|--replace]   one Dock icon per account; --replace swaps out the generic
                                Claude/Codex icons so each icon is exactly one account
  dial desktop                  desktop app status
  dial desktop add claude       sign another account into Claude Desktop (opens it in its own window)
  dial desktop login <account>  renew an account's Claude Desktop sign-in
  dial desktop use <account>    make <account> the main Claude window's account

REMOTES
  dial remote add <name> <ssh-target> [--providers claude,codex] [--manual]
  dial remote ls | rm <name> | status [name]
  dial remote seed <name> <account|--all>   give the remote its own login
  dial remote sync [name]       make remotes match the active accounts

MORE
  dial usage [account…] [--refresh]   usage only
  dial watch [--interval 60]    live status view
  dial waybar                   JSON for waybar/polybar
  dial keepalive                refresh idle logins before they expire
  dial follow                   make remotes follow account changes made outside dial
  dial doctor                   check setup
  dial config [key [value]]
  dial version

<account> is an id, label, email, list number (claude:2), or a prefix.
Add --json to most commands for machine-readable output.
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		cmdStatus(nil)
		return
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "status", "st", "s":
		cmdStatus(rest)
	case "use", "switch", "u":
		cmdUse(rest)
	case "claude", "codex", "cc", "cx":
		if len(rest) == 0 {
			p, _ := vault.ParseProvider(cmd)
			cmdStatus([]string{"--provider", string(p)})
			return
		}
		p, _ := vault.ParseProvider(cmd)
		cmdUse(append([]string{string(p) + ":" + rest[0]}, rest[1:]...))
	case "next":
		cmdCycle(rest, false)
	case "best":
		cmdCycle(rest, true)
	case "add", "new":
		cmdAdd(rest)
	case "login", "relogin":
		cmdLogin(rest)
	case "import":
		cmdImport(rest)
	case "ls", "list":
		cmdList(rest)
	case "rename", "mv", "label":
		cmdRename(rest)
	case "rm", "remove", "delete":
		cmdRemove(rest)
	case "run", "exec":
		cmdRun(rest)
	case "env":
		cmdEnv(rest)
	case "open", "window":
		cmdOpen(rest)
	case "show", "focus":
		cmdDesktop(append([]string{"show"}, rest...))
	case "dock":
		cmdDock(rest)
	case "desktop", "app":
		cmdDesktop(rest)
	case "remote", "remotes":
		cmdRemote(rest)
	case "remote-agent":
		cmdRemoteAgent(rest)
	case "usage":
		cmdUsage(rest)
	case "watch", "top":
		cmdWatch(rest)
	case "waybar", "bar":
		cmdWaybar(rest)
	case "keepalive", "refresh":
		cmdKeepAlive(rest)
	case "follow":
		cmdFollow(rest)
	case "doctor":
		cmdDoctor(rest)
	case "config":
		cmdConfig(rest)
	case "version", "--version", "-v":
		fmt.Println("dial " + version)
	case "help", "--help", "-h":
		fmt.Print(usageText)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		fmt.Print(usageText)
		os.Exit(2)
	}
}

// opts is a tiny flag parser that accepts flags anywhere.
type opts struct {
	pos   []string
	flags map[string]string
	rest  []string // after --
}

func parse(args []string, valued ...string) opts {
	o := opts{flags: map[string]string{}}
	isValued := map[string]bool{}
	for _, v := range valued {
		isValued[v] = true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			o.rest = args[i+1:]
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			name := strings.TrimLeft(a, "-")
			if k, v, ok := strings.Cut(name, "="); ok {
				o.flags[k] = v
				continue
			}
			if isValued[name] && i+1 < len(args) {
				o.flags[name] = args[i+1]
				i++
				continue
			}
			o.flags[name] = "true"
			continue
		}
		o.pos = append(o.pos, a)
	}
	return o
}

func (o opts) has(names ...string) bool {
	for _, n := range names {
		if _, ok := o.flags[n]; ok {
			return true
		}
	}
	return false
}

func (o opts) get(name string) string { return o.flags[name] }

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func die(err error) {
	if err != nil {
		ui.Fatal(err)
	}
}

func dief(format string, a ...any) { ui.Fatal(fmt.Errorf(format, a...)) }
