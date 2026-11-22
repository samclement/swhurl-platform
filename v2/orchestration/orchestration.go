// Package orchestration runs the platform's use cases. A use case assembles the inputs it needs, calls the
// kernel's decisions in order, performs its one effect through an adapter, and reports either the result or
// where it stopped.
//
// It decides nothing itself: every rule it applies is a kernel rule, and a refusal is the kernel's own. It
// prints nothing, parses no arguments, chooses no exit code and names no flag — the command above it does
// those — so the same use case can be called by a command, a test or a future console handler.
package orchestration
