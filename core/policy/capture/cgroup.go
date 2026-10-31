package capture

import "strings"

// parseCgroup extracts the cgroup v2 path from /proc/<pid>/cgroup
// contents ("0::/system.slice/hy2scale.service"). The root ("/") is
// refused: matching it would exclude every process of the (namespace)
// root, i.e. nothing would be captured.
func parseCgroup(s string) (path, reason string) {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			if rest == "" || rest == "/" {
				return "", ReasonCgroupRoot
			}
			if strings.ContainsAny(rest, " \t'\"") {
				return "", ReasonXtCgroup
			}
			return rest, ""
		}
	}
	return "", ReasonCgroupV1
}

// ParseCgroup is parseCgroup for tests.
func ParseCgroup(s string) (path, reason string) { return parseCgroup(s) }
