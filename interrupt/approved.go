package interrupt

import "context"

type approvedExecuteKey struct{}

// WithApprovedExecute marks ctx as an interrupt resume that is about to
// run original tool handlers for pending calls the human approved
// (Decision.Execute && !Decision.IsError). Host permission layers that
// hard-reject Dangerous tools in non-interactive mode consult
// IsApprovedExecute so the approved handler can actually run, without
// widening that skip to agents that never froze the batch.
func WithApprovedExecute(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, approvedExecuteKey{}, true)
}

// IsApprovedExecute reports whether ctx was produced by WithApprovedExecute.
func IsApprovedExecute(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(approvedExecuteKey{}).(bool)
	return v
}
