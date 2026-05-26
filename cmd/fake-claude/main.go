// fake-claude is an env-driven stand-in for `claude -p` used in wrap's
// integration tests. Phase 1 supports the bare minimum required for tests
// that spawn the binary as a subprocess; later phases will extend it to
// emit scripted MCP tool calls.
//
// Environment variables:
//   FAKE_CLAUDE_EXIT_CODE   integer exit code to use (default 0)
//   FAKE_CLAUDE_SLEEP_MS    milliseconds to sleep before exiting (default 0)
//   FAKE_CLAUDE_STDOUT      string to print to stdout before exiting
//   FAKE_CLAUDE_STDERR      string to print to stderr before exiting
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	if s := os.Getenv("FAKE_CLAUDE_SLEEP_MS"); s != "" {
		if ms, err := strconv.Atoi(s); err == nil && ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
	}
	if s := os.Getenv("FAKE_CLAUDE_STDOUT"); s != "" {
		fmt.Fprint(os.Stdout, s)
	}
	if s := os.Getenv("FAKE_CLAUDE_STDERR"); s != "" {
		fmt.Fprint(os.Stderr, s)
	}
	code := 0
	if s := os.Getenv("FAKE_CLAUDE_EXIT_CODE"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			code = n
		}
	}
	os.Exit(code)
}
