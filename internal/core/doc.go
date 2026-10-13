// Package core makes authorization decisions from policy, the event log and
// a call. Its functions are pure; callers pass the log as a slice.
//
// Sessions exposed to untrusted content are "contaminated". Sessions with
// private data are "private". Decide uses session history and tool classes
// to return run, hold for approval, or refuse.
//
// Package layout:
//
//   - labels.go: contamination and privacy labels.
//   - policy.go: configuration and tool classes.
//   - event.go: event types and log validation.
//   - state.go: session and source labels derived from the log.
//   - decide.go: call authorization.
//   - approve.go: proposals and approval decisions.
//
// Purity tests prohibit I/O and clock access. The surrounding packages,
// including eventlog and gateway, execute the decisions.
package core
