package osc

import "fmt"

// Begin registers the pane in attention trackers without showing a fake turn:
// a short working record followed by idle, matching the shim's launch semantics.
func Begin(app string) {
	BeginWithProfile(app, "")
}

// BeginWithProfile registers the pane like Begin, carrying an optional profile
// label on the OSC 7501 records.
func BeginWithProfile(app, profile string) {
	EmitWithProfile(app, profile, "working", "", "")
	EmitWithProfile(app, profile, "idle", "", "")
}

// Prime is an alias for Begin kept for callers migrating from harness/attn.
func Prime(app string) {
	Begin(app)
}

// Finish reports the terminal state for an exit code: 0 -> done with
// message "finished"; any other code -> error with "exited with status N".
// The message travels base64-encoded in the msg field.
func Finish(app string, exitCode int) {
	FinishWithProfile(app, "", exitCode)
}

// FinishWithProfile reports the terminal state like Finish, carrying an
// optional profile label on the OSC 7501 record.
func FinishWithProfile(app, profile string, exitCode int) {
	if exitCode == 0 {
		EmitWithProfile(app, profile, "done", "finished", "")
		return
	}
	EmitWithProfile(app, profile, "error", fmt.Sprintf("exited with status %d", exitCode), "")
}
