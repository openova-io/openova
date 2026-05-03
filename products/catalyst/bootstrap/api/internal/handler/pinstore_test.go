package handler

import (
	"sync"
	"testing"
	"time"
)

// frozenClock returns a (nowFn, advance) pair the tests use to control
// the pinStore's notion of "now" without sleeping.
func frozenClock(start time.Time) (func() time.Time, func(d time.Duration)) {
	mu := sync.Mutex{}
	cur := start
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return cur
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		cur = cur.Add(d)
	}
	return now, advance
}

func TestPinStore_PutAndVerifyHappy(t *testing.T) {
	s := newPinStoreNoSweeper()
	s.put("op@example.com", "123456", "req-1")
	r, _ := s.verify("op@example.com", "123456", "req-1")
	if r != verifyOK {
		t.Fatalf("verify: got %v want verifyOK", r)
	}
	if s.size() != 0 {
		t.Fatalf("size: got %d want 0 (entry should be deleted on match)", s.size())
	}
}

func TestPinStore_VerifyEmailCaseInsensitive(t *testing.T) {
	s := newPinStoreNoSweeper()
	s.put("Op@Example.com", "123456", "req-1")
	r, _ := s.verify("op@example.com", "123456", "req-1")
	if r != verifyOK {
		t.Fatalf("case-insensitive verify: got %v want verifyOK", r)
	}
}

func TestPinStore_VerifyNotFound(t *testing.T) {
	s := newPinStoreNoSweeper()
	r, _ := s.verify("nobody@example.com", "123456", "req-x")
	if r != verifyNotFound {
		t.Fatalf("verify: got %v want verifyNotFound", r)
	}
}

func TestPinStore_VerifyWrongPINIncrementsAttempts(t *testing.T) {
	s := newPinStoreNoSweeper()
	s.put("op@example.com", "123456", "req-1")
	r, remaining := s.verify("op@example.com", "000000", "req-1")
	if r != verifyWrongPIN {
		t.Fatalf("first wrong: got %v want verifyWrongPIN", r)
	}
	if remaining != 2 {
		t.Fatalf("remaining attempts after 1 wrong: got %d want 2", remaining)
	}
	r, remaining = s.verify("op@example.com", "000000", "req-1")
	if r != verifyWrongPIN || remaining != 1 {
		t.Fatalf("second wrong: got %v/%d want verifyWrongPIN/1", r, remaining)
	}
}

func TestPinStore_VerifyLocksOnThirdWrong(t *testing.T) {
	s := newPinStoreNoSweeper()
	s.put("op@example.com", "123456", "req-1")
	for i := 0; i < 2; i++ {
		s.verify("op@example.com", "000000", "req-1")
	}
	r, _ := s.verify("op@example.com", "000000", "req-1")
	if r != verifyAttemptsLocked {
		t.Fatalf("third wrong: got %v want verifyAttemptsLocked", r)
	}
	if s.size() != 0 {
		t.Fatalf("size after lock: got %d want 0", s.size())
	}
	// A subsequent verify with the correct PIN must NOT succeed — the
	// entry has been deleted.
	r, _ = s.verify("op@example.com", "123456", "req-1")
	if r != verifyNotFound {
		t.Fatalf("verify after lock: got %v want verifyNotFound", r)
	}
}

func TestPinStore_VerifyExpired(t *testing.T) {
	now, advance := frozenClock(time.Now())
	s := newPinStoreNoSweeper()
	s.now = now
	s.put("op@example.com", "123456", "req-1")
	advance(pinTTL + time.Second)
	r, _ := s.verify("op@example.com", "123456", "req-1")
	if r != verifyExpired {
		t.Fatalf("expired verify: got %v want verifyExpired", r)
	}
	if s.size() != 0 {
		t.Fatalf("size after expired verify: got %d want 0", s.size())
	}
}

func TestPinStore_VerifyRequestMismatch(t *testing.T) {
	s := newPinStoreNoSweeper()
	s.put("op@example.com", "123456", "req-A")
	r, _ := s.verify("op@example.com", "123456", "req-B")
	if r != verifyRequestMismatch {
		t.Fatalf("request mismatch: got %v want verifyRequestMismatch", r)
	}
	if s.size() != 1 {
		t.Fatalf("size after mismatch: got %d want 1", s.size())
	}
}

func TestPinStore_CanIssueRateLimit(t *testing.T) {
	now, advance := frozenClock(time.Now())
	s := newPinStoreNoSweeper()
	s.now = now

	ok, _ := s.canIssue("op@example.com")
	if !ok {
		t.Fatal("first canIssue: expected true")
	}
	s.put("op@example.com", "123456", "req-1")

	ok, retry := s.canIssue("op@example.com")
	if ok {
		t.Fatal("immediate re-issue: expected rate-limited")
	}
	if retry <= 0 || retry > pinIssueCooldown {
		t.Fatalf("retryAfter: got %v want 0 < r <= %v", retry, pinIssueCooldown)
	}

	advance(pinIssueCooldown - 1*time.Second)
	ok, _ = s.canIssue("op@example.com")
	if ok {
		t.Fatal("just before cooldown: expected rate-limited")
	}

	advance(2 * time.Second)
	ok, _ = s.canIssue("op@example.com")
	if !ok {
		t.Fatal("after cooldown: expected allowed")
	}
}

func TestPinStore_DropClearsEntry(t *testing.T) {
	s := newPinStoreNoSweeper()
	s.put("op@example.com", "123456", "req-1")
	if s.size() != 1 {
		t.Fatalf("size before drop: got %d want 1", s.size())
	}
	s.drop("op@example.com")
	if s.size() != 0 {
		t.Fatalf("size after drop: got %d want 0", s.size())
	}
	s.drop("op@example.com")
}

func TestPinStore_SweepRemovesExpired(t *testing.T) {
	now, advance := frozenClock(time.Now())
	s := newPinStoreNoSweeper()
	s.now = now
	s.put("a@example.com", "111111", "r1")
	s.put("b@example.com", "222222", "r2")
	if s.size() != 2 {
		t.Fatalf("size: got %d want 2", s.size())
	}
	advance(pinTTL + time.Second)
	removed := s.sweep()
	if removed != 2 {
		t.Fatalf("sweep removed: got %d want 2", removed)
	}
	if s.size() != 0 {
		t.Fatalf("size after sweep: got %d want 0", s.size())
	}
}

func TestPinStore_NormalizeKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"op@example.com", "op@example.com"},
		{"OP@EXAMPLE.COM", "op@example.com"},
		{" Op@Example.com ", "op@example.com"},
		{"\tOp@Example.com\n", "op@example.com"},
	}
	for _, c := range cases {
		got := normalizePinKey(c.in)
		if got != c.want {
			t.Errorf("normalizePinKey(%q): got %q want %q", c.in, got, c.want)
		}
	}
}

func TestPinStore_SweeperLifecycle(t *testing.T) {
	s, stop := newPinStore()
	if s == nil {
		t.Fatal("newPinStore: nil")
	}
	stop()
}
