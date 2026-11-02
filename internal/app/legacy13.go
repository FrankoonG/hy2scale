package app

import (
	"log"
	"regexp"
	"strings"

	"github.com/FrankoonG/hy2scale/core/netfilter"
)

// A 1.3.x node wrote its rule capture and L2TP redirect straight into the
// nat table's built-in chains and removed them only on a clean stop. An
// in-place upgrade ends that process without it, and on a host-network
// node the rules outlive the process: 1.4.x captures the flow first, then
// the stale DNAT rewrites it to the old proxy port (127.0.0.99:12380 /
// 12381), where nothing listens any more, and every rule target fails.
// cleanupV13Rules removes exactly those rules, from every iptables binary
// that may have held them; nothing 1.4.x installs looks like them.
var v13Leftover = regexp.MustCompile(`^-A (OUTPUT|PREROUTING) (` +
	// rule capture: the transparent proxy ports
	`.*-j DNAT --to-destination 127\.0\.0\.99:1238[01]` +
	// the bypass of the old proxy's own dials
	`|-p (tcp|udp) -m mark --mark 0x1234 -j RETURN` +
	// L2TP (kernel ppp) to the old transparent proxy
	`|-i ppp\+ .*-j DNAT --to-destination \S+` +
	`)$`)

func cleanupV13Rules() {
	if ok, _ := netfilter.CheckCapability(); !ok {
		return
	}
	seen := map[string]bool{}
	for _, prog := range []string{netfilter.Prog(false), "iptables", "iptables-legacy"} {
		if prog == "" || seen[prog] {
			continue
		}
		seen[prog] = true
		out, err := netfilter.Exec(prog, "-t", "nat", "-S").Output()
		if err != nil {
			continue
		}
		n := 0
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if !v13Leftover.MatchString(line) {
				continue
			}
			args := strings.Fields(line)
			args[0] = "-D"
			if netfilter.Exec(prog, append([]string{"-t", "nat"}, args...)...).Run() == nil {
				n++
			}
		}
		if n > 0 {
			log.Printf("[policy] removed %d rules left in the nat table by a 1.3.x process (%s)", n, prog)
		}
	}
}
