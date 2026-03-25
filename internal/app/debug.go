package app

import (
	"log"
	"os"
	"strings"
	"sync"
)

// debugMode is true when DEBUG=true or DEBUG=1 is set in the environment.
var debugMode = sync.OnceValue(func() bool {
	v := strings.ToLower(os.Getenv("DEBUG"))
	return v == "true" || v == "1" || v == "yes"
})

// debugLog prints a log message only when DEBUG mode is enabled.
func debugLog(format string, args ...any) {
	if debugMode() {
		log.Printf(format, args...)
	}
}
