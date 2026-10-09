// Package core is the part of cleanroom that decides. It is a functional
// core: every function here is pure, and the only state is the event log,
// passed in as a slice.
//
// The idea in one paragraph. A session that has read untrusted content may
// be following that content's instructions, so it is "contaminated". A
// session that holds the user's data is "private". Every call a session
// makes goes through Decide, which looks at what the session has read so
// far and at what the tool can do, and answers run, hold (ask the person)
// or refuse.
//
// What is where:
//
//   - labels.go: the two labels, contaminated and private.
//   - policy.go: the trusted configuration and the four tool classes.
//   - event.go: the log's events. The log is the only state.
//   - state.go: folds that compute a session's labels from the log.
//   - decide.go: Decide, the decision itself.
//   - approve.go: held calls and the person's approval of them.
//
// Nothing in this package reads a file, a clock or the network, and a test
// fails if it ever imports a package that could. The shell around it
// (packages eventlog, gateway and the rest) does the I/O and asks this
// package what to do.
package core
