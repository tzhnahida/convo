// Package clock provides a single injectable time source for the application.
//
// Every scheduling decision (due-item selection, daily target boundaries,
// streak/daily-stats keys, SM-2 next-review offsets, expansion day seeds)
// MUST go through clock.Now / clock.Today so that:
//
//  1. The app never mixes two clocks (Go time.Now vs SQLite datetime('now'))
//     — a boundary bug the old code had (a review submitted at 23:59:59 could
//     be counted on the wrong day depending on which side of the query did
//     the formatting).
//  2. Tests and the month-long simulation harness can time-travel by
//     overriding clock.Now (which also time-travels every derived decision).
package clock

import (
	"context"
	"time"
)

// Now returns the application's current time. Override in tests/simulation
// by replacing this function var. It must be restored after use.
var Now = func() time.Time { return time.Now() }

// Today returns the application's local calendar date (YYYY-MM-DD).
func Today() string { return Now().Format("2006-01-02") }

// TodayTS returns the application's full local timestamp (YYYY-MM-DD HH:MM:SS),
// the format used for SQLite TEXT timestamp comparisons.
func TodayTS() string { return Now().Format("2006-01-02 15:04:05") }

// Sleep waits for d or until ctx is done. Override in tests/simulation to make
// waits virtual (a no-op): the simulation grid is one minute, so second-level
// "human pace" delays can neither be represented nor afforded -- real sleeps
// turned a 30-day run into hours.
var Sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
