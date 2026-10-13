package main

import (
	"path/filepath"
	"regexp"
	"strings"
)

// locationFlags is the --policy and --state to put in a cleanroom command
// that a hint names, so that the command reaches the policy and state this
// one used. It is empty for the defaults. Flags come before a command's
// other arguments, which is where the flag package looks for them.
func locationFlags(policyFlag, stateFlag string) string {
	var b strings.Builder
	if policyFlag != "" {
		b.WriteString(" --policy " + shellQuote(policyFlag))
	}
	if stateFlag != "" {
		b.WriteString(" --state " + shellQuote(stateFlag))
	}
	return b.String()
}

// hintFlags is locationFlags for the flags this command was given, as
// absolute paths. A hint may be followed from another directory, and one
// that reaches an agent is passed on to a person somewhere else.
func (c *common) hintFlags() string {
	policyFlag, stateDir, err := c.absoluteFlags()
	if err != nil {
		policyFlag, stateDir = c.policyPath, c.stateDir
	}
	return locationFlags(policyFlag, stateDir)
}

// childLogPath is where this command's state keeps what readers and
// upstreams write to stderr.
func (c *common) childLogPath() (string, error) {
	policyPath, err := c.resolvedPolicyPath()
	if err != nil {
		return "", err
	}
	dir, err := c.stateDirFor(filepath.Dir(policyPath))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, childLogName), nil
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellQuote returns s as one word of a POSIX shell command.
func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
