// Package clock provides a tiny Clock interface used by handlers and the
// builder to evaluate "now". Production wires clock.System; tests inject
// clock.Fake (or a mockery-generated mock) so time-sensitive code paths are
// deterministic without needing test-only setters on production types.
package clock

import "time"

// Clock returns the current time. Implementations must be safe for
// concurrent use.
type Clock interface {
	Now() time.Time
}

// System is the production Clock backed by time.Now.
type System struct{}

// Now returns time.Now in UTC.
func (System) Now() time.Time { return time.Now() }

// Fake is a Clock that always returns T. Useful for tests pinned to a fixed
// timestamp anchor (see internal/integration/fixedNow).
type Fake struct{ T time.Time }

// Now returns the configured fixed time.
func (f Fake) Now() time.Time { return f.T }
