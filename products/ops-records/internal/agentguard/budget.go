package agentguard

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrBudgetSpent is returned when a key has used every turn of the current window.
var ErrBudgetSpent = errors.New("agentguard: turn budget spent")

// ErrBudgetUnknown is returned when the budget cannot be counted: it has no limit, no window
// or no counter, the key is empty, or the counter failed or gave a count below one.
var ErrBudgetUnknown = errors.New("agentguard: turn budget cannot be counted")

// Counter counts turns per key and window.
type Counter interface {
	// Add adds one turn for key in the window that starts at start and returns the number of
	// turns of that window after adding.
	Add(ctx context.Context, key string, start time.Time) (int, error)
}

// Budget allows at most Turns turns per key in each window of length Window. Windows start
// at multiples of Window since the zero time. A key names what is counted, for example an
// Organization and an agent.
//
// Budget fails closed: Take allows a turn only when the counter has counted it within the
// limit. Every other case refuses the turn.
type Budget struct {
	Turns   int
	Window  time.Duration
	Counter Counter
	Now     func() time.Time // nil means time.Now
}

// Take counts one turn for key. It returns nil when the turn is allowed, an error that wraps
// ErrBudgetSpent when the limit is reached, and an error that wraps ErrBudgetUnknown in every
// other case.
func (b *Budget) Take(ctx context.Context, key string) error {
	if b == nil || b.Turns <= 0 || b.Window <= 0 || b.Counter == nil || key == "" {
		return ErrBudgetUnknown
	}
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	start := now().UTC().Truncate(b.Window)
	n, err := b.Counter.Add(ctx, key, start)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBudgetUnknown, err)
	}
	if n < 1 {
		return fmt.Errorf("%w: count %d", ErrBudgetUnknown, n)
	}
	if n > b.Turns {
		return fmt.Errorf("%w: %d of %d turns for %q", ErrBudgetSpent, n, b.Turns, key)
	}
	return nil
}

// MemoryCounter is a Counter in memory, for one process and for tests. It keeps the newest
// window of each key. A turn whose window is older than the newest one counts in the newest.
type MemoryCounter struct {
	mu   sync.Mutex
	keys map[string]memoryWindow
}

type memoryWindow struct {
	start time.Time
	n     int
}

// Add counts one turn. It fails when ctx is done.
func (c *MemoryCounter) Add(ctx context.Context, key string, start time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keys == nil {
		c.keys = make(map[string]memoryWindow)
	}
	w := c.keys[key]
	if start.After(w.start) {
		w = memoryWindow{start: start}
	}
	w.n++
	c.keys[key] = w
	return w.n, nil
}
