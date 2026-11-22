//go:build linux

package api

import (
	"log"
	"net/netip"
	"os"
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
	if len(done) == 0 {
		return nil
	}
	log.Printf("[remote-access] firewall: %s/%d let in from %s (%s)", proto, port, visitor.Unmap(), strings.Join(done, ", "))
	return func() {
		for _, prog := range done {
			for i := 0; i < 4 && netfilter.Exec(prog, append([]string{"-D"}, rule...)...).Run() == nil; i++ {
			}
		}
	}
}

// raFWSweep removes rules left by an earlier run.
func raFWSweep() {
	if raFWMode() == "off" {
		return
	}
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
