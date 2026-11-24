//go:build linux

package api

import (
	"log"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/FrankoonG/hy2scale/core/netfilter"
)

// Remote access ports and the host firewall: on the host network, a port
// opened for one visitor is let through the INPUT chain for that visitor
// only, by a rule tagged "hy2scale-ra:<id>" (removed when the port closes;
// left-overs of an earlier run are removed at start). A chain that lets
// everything in (policy ACCEPT, no rules) is left alone, and so is every
// iptables variant this host does not filter with.
// HY2SCALE_RA_FIREWALL=force applies the rules off the host network too
// (tests); =off never touches the firewall.

const raFWTag = "hy2scale-ra:"

func raFWMode() string { return strings.ToLower(os.Getenv("HY2SCALE_RA_FIREWALL")) }

// raFWProgs: the iptables variants present here (v6: ip6tables).
var raFWProgs = sync.OnceValue(func() [2][]string {
	var out [2][]string
	for i, cands := range [][]string{
		{netfilter.Prog(false), "iptables-legacy", "iptables-nft"},
		{netfilter.Prog(true), "ip6tables-legacy", "ip6tables-nft"},
	} {
		seen := map[string]bool{}
		for _, p := range cands {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			if netfilter.Exec(p, "-S", "INPUT").Run() == nil {
				out[i] = append(out[i], p)
			}
		}
	}
	return out
})

// raFWFilters: whether a variant's INPUT chain filters anything.
func raFWFilters(prog string) bool {
	out, err := netfilter.Exec(prog, "-S", "INPUT").Output()
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || l == "-P INPUT ACCEPT" || strings.Contains(l, raFWTag) {
			continue
		}
		return true
	}
	return false
}

// raFWOpen lets visitor reach port/proto through the host firewall; it
// returns the undo (nil: nothing was needed or possible).
func raFWOpen(id, proto string, visitor netip.Addr, port int) func() {
	mode := raFWMode()
	if mode == "off" || (mode != "force" && !netfilter.CheckHostNetwork()) || !visitor.IsValid() {
		return nil
	}
	v6 := visitor.Unmap().Is6()
	fam := 0
	if v6 {
		fam = 1
	}
	rule := []string{"INPUT", "-s", visitor.Unmap().String(), "-p", proto, "--dport", strconv.Itoa(port),
		"-m", "comment", "--comment", raFWTag + id, "-j", "ACCEPT"}
	var undo []func()
	if f := raFW4Open(id, proto, visitor.Unmap(), port); f != nil {
		undo = append(undo, f)
	}
	var done []string
	for _, prog := range raFWProgs()[fam] {
		if !raFWFilters(prog) {
			continue
		}
		if out, err := netfilter.Exec(prog, append([]string{"-I"}, rule...)...).CombinedOutput(); err != nil {
			log.Printf("[remote-access] %s: firewall rule for %s: %v %s", prog, visitor, err, strings.TrimSpace(string(out)))
			continue
		}
		done = append(done, prog)
	}
	if len(done) > 0 {
		log.Printf("[remote-access] firewall: %s/%d let in from %s (%s)", proto, port, visitor.Unmap(), strings.Join(done, ", "))
		undo = append(undo, func() {
			for _, prog := range done {
				for i := 0; i < 4 && netfilter.Exec(prog, append([]string{"-D"}, rule...)...).Run() == nil; i++ {
				}
			}
		})
	}
	if len(undo) == 0 {
		return nil
	}
	return func() {
		for _, f := range undo {
			f()
		}
	}
}

// OpenWrt's fw4 filters in its own nftables table, which an iptables rule
// cannot let anything through: there the rule goes first in fw4's input
// chain. A firewall reload rebuilds that table and drops it.

var raFW4 = sync.OnceValue(func() bool {
	return exec.Command("nft", "list", "chain", "inet", "fw4", "input").Run() == nil
})

func raFW4Open(id, proto string, visitor netip.Addr, port int) func() {
	if !raFW4() {
		return nil
	}
	fam := "ip"
	if visitor.Is6() {
		fam = "ip6"
	}
	tag := raFWTag + id
	args := []string{"insert", "rule", "inet", "fw4", "input", fam, "saddr", visitor.String(),
		proto, "dport", strconv.Itoa(port), "counter", "accept", "comment", `"` + tag + `"`}
	if out, err := exec.Command("nft", args...).CombinedOutput(); err != nil {
		log.Printf("[remote-access] fw4: rule for %s: %v %s", visitor, err, strings.TrimSpace(string(out)))
		return nil
	}
	log.Printf("[remote-access] firewall: %s/%d let in from %s (fw4)", proto, port, visitor)
	return func() { raFW4Remove(func(c string) bool { return c == tag }) }
}

var raFW4Handle = regexp.MustCompile(`comment "([^"]*)".*# handle (\d+)`)

// raFW4Remove deletes the tagged rules of fw4's input chain whose tag match
// picks.
func raFW4Remove(match func(tag string) bool) {
	if !raFW4() {
		return
	}
	out, err := exec.Command("nft", "-a", "list", "chain", "inet", "fw4", "input").Output()
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(out), "\n") {
		m := raFW4Handle.FindStringSubmatch(l)
		if m == nil || !strings.HasPrefix(m[1], raFWTag) || !match(m[1]) {
			continue
		}
		exec.Command("nft", "delete", "rule", "inet", "fw4", "input", "handle", m[2]).Run()
	}
}

// raFWSweep removes rules left by an earlier run.
func raFWSweep() {
	if raFWMode() == "off" {
		return
	}
	raFW4Remove(func(string) bool { return true })
	for _, progs := range raFWProgs() {
		for _, prog := range progs {
			out, err := netfilter.Exec(prog, "-S", "INPUT").Output()
			if err != nil {
				continue
			}
			for _, l := range strings.Split(string(out), "\n") {
				if !strings.HasPrefix(l, "-A INPUT") || !strings.Contains(l, raFWTag) {
					continue
				}
				args := strings.Fields(strings.TrimPrefix(l, "-A "))
				for i, a := range args {
					args[i] = strings.Trim(a, `"`)
				}
				if netfilter.Exec(prog, append([]string{"-D"}, args...)...).Run() == nil {
					log.Printf("[remote-access] firewall: removed left-over rule (%s)", prog)
				}
			}
		}
	}
}
