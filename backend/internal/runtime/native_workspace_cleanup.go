package runtime

import (
	"context"
	"log/slog"
	"time"
)

// RunCleanup belongs to the explicitly enabled host lifecycle. It does not
// authorize a user or Agent to sweep environments, or provision new ones.
func (manager *NativeWorkspaceManager) RunCleanup(ctx context.Context, interval time.Duration, logger *slog.Logger) error {
	if !nativeJanitorIdentity(ctx) {
		return domainError("AGENT_ACTIVITY_FORBIDDEN", "Workspace cleanup requires the host service identity.")
	}
	if interval < time.Second || interval > time.Hour {
		return domainError("NATIVE_WORKSPACE_REQUEST_INVALID", "Cleanup interval must be between one second and one hour.")
	}
	if logger == nil {
		logger = slog.Default()
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return nil
		}
		// Bound the whole batch, not just each individual engine operation.
		cycle, cancel := context.WithTimeout(ctx, 2*time.Minute)
		report, err := manager.Sweep(cycle, 8)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil || report.Pending > 0 {
			logger.Warn("native workspace cleanup pending", "attempted", report.Attempted,
				"confirmed", report.Confirmed, "pending", report.Pending, "sweep_failed", err != nil)
		} else if report.Confirmed > 0 {
			logger.Info("native workspace cleanup confirmed", "confirmed", report.Confirmed)
		}
		// Start a new delay after completion; slow engine calls cannot accumulate
		// overlapping sweeps or an unbounded ticker backlog.
		timer.Reset(interval)
	}
}
