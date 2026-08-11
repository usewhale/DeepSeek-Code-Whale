package app

import (
	"fmt"
	"sync"
	"testing"
)

// TestSessionIDConcurrentReadWrite guards the sessionID synchronization used
// by the MCP startup goroutine: RestorePromotedTools (and the promoted-tool
// persistence path) read the current session while the dispatch goroutine
// switches sessions via resume / session-new / fork. Readers must never see a
// torn write; run with -race.
func TestSessionIDConcurrentReadWrite(t *testing.T) {
	a := &App{sessionsDir: t.TempDir(), sessionID: "s0"}

	var wg sync.WaitGroup
	// Readers mirror the async MCP startup path (sessionPath snapshot + the
	// public SessionID accessor used by other goroutines).
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				dir, id := a.sessionPath()
				if dir == "" || id == "" {
					t.Errorf("sessionPath returned empty snapshot")
					return
				}
				if a.SessionID() == "" {
					t.Errorf("SessionID returned empty")
					return
				}
			}
		}()
	}
	// Writers mirror the dispatch paths that switch sessions.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				a.setSessionID(fmt.Sprintf("s%d-%d", n, j%8))
			}
		}(i)
	}
	wg.Wait()
	if a.SessionID() == "" {
		t.Fatalf("sessionID lost after concurrent writes")
	}
}
