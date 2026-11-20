package main

import (
	"log"
	"os"

	"golang.org/x/sys/unix"
)

// noHugePages turns transparent huge pages off for this process. With THP
// "always" (iKuai and other routers), khugepaged merges memory the Go
// runtime has returned back into huge pages, so after a traffic peak the
// resident size keeps growing towards the peak while the heap stays small
// (https://go.dev/issue/64332). HY2SCALE_THP=1 leaves THP as configured.
func noHugePages() {
	if os.Getenv("HY2SCALE_THP") == "1" {
		return
	}
	if err := unix.Prctl(unix.PR_SET_THP_DISABLE, 1, 0, 0, 0); err != nil {
		log.Printf("[runtime] disabling transparent huge pages: %v", err)
	}
}
