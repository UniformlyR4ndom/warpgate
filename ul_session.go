package main

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const (
	// ulSessionIdleExpiry bounds how long a chunked-upload session (or a
	// completed-session marker) may sit with no activity before being
	// purged.
	ulSessionIdleExpiry = 300 * time.Second
	// ulChunkProcessTimeout bounds how long a single chunk's request body
	// may take to arrive before the client is presumed stalled. Independent
	// of ulSessionIdleExpiry even though both are currently 300s.
	ulChunkProcessTimeout = 300 * time.Second

	ulSweepInterval = 30 * time.Second
)

// ulSessionCore is the state shared by every resumable, chunked,
// continuation-key-protected upload: real files under /api/files/upload and
// the /api/sim/upload benchmark probe both use it, differing only in what a
// chunk actually does with its bytes and what "abort" means for whatever's
// behind the session (a file on disk vs. an in-memory hash). A completed
// session keeps currentKey as the terminal key (previousKey unused) so a
// late retry of the final chunk's ack can still be answered idempotently
// instead of being rejected.
type ulSessionCore struct {
	processing  bool
	completed   bool
	currentKey  string
	previousKey string
	expiresAt   time.Time
}

func (c *ulSessionCore) core() *ulSessionCore { return c }

// ulCoreHolder is implemented by any session payload type usable with
// sessionStore, via an embedded ulSessionCore.
type ulCoreHolder interface {
	core() *ulSessionCore
}

type ulKeyResult int

const (
	ulKeyUnrecognized ulKeyResult = iota
	ulKeyCurrent
	ulKeyPrevious
	ulKeyCompleted
)

func ulNewKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ulValidateKey classifies key against sess's recorded state. Callers must
// hold the processing claim for sess's name (via sessionStore.tryClaim)
// before calling this.
func ulValidateKey(sess ulCoreHolder, key string) ulKeyResult {
	c := sess.core()
	if c.completed {
		if key == c.currentKey {
			return ulKeyCompleted
		}
		return ulKeyUnrecognized
	}
	if c.currentKey != "" && key == c.currentKey {
		return ulKeyCurrent
	}
	if c.previousKey != "" && key == c.previousKey {
		return ulKeyPrevious
	}
	return ulKeyUnrecognized
}

// sessionStore is a concurrency-safe, keyed collection of chunked-upload
// sessions. Each feature (real uploads, sim upload) owns its own instance —
// a name in one store has no relationship to the same name in another.
type sessionStore[T ulCoreHolder] struct {
	mu       sync.Mutex
	sessions map[string]T
}

func newSessionStore[T ulCoreHolder]() *sessionStore[T] {
	return &sessionStore[T]{sessions: make(map[string]T)}
}

// tryClaim acquires exclusive processing rights to name, rejecting outright
// (ok=false) if another request already holds them. wasNew reports whether
// newT was called to create a fresh entry: callers use it to decide whether
// an unrecognized continuation-key should discard the entry (nothing
// legitimate to preserve) or leave an unrelated, already-legitimate
// session/completion record untouched.
func (st *sessionStore[T]) tryClaim(name string, newT func() T) (sess T, wasNew bool, ok bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	sess, exists := st.sessions[name]
	if exists && sess.core().processing {
		var zero T
		return zero, false, false
	}
	if !exists {
		sess = newT()
		st.sessions[name] = sess
		wasNew = true
	}
	sess.core().processing = true
	return sess, wasNew, true
}

// release clears the processing claim on name. If discard is true the entry
// is removed entirely; otherwise its fields are left exactly as the caller
// last set them.
func (st *sessionStore[T]) release(name string, discard bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if discard {
		delete(st.sessions, name)
		return
	}
	if sess, ok := st.sessions[name]; ok {
		sess.core().processing = false
	}
}

// lockedNames returns the names with a non-completed session currently on
// record, so in-progress uploads can be hidden from listings.
func (st *sessionStore[T]) lockedNames() map[string]struct{} {
	st.mu.Lock()
	defer st.mu.Unlock()
	hidden := make(map[string]struct{}, len(st.sessions))
	for name, sess := range st.sessions {
		if !sess.core().completed {
			hidden[name] = struct{}{}
		}
	}
	return hidden
}

// sweepExpired purges sessions idle longer than ulSessionIdleExpiry, never
// touching one currently being processed. onAbort, if non-nil, is called
// (outside the lock) for each purged non-completed session, so the caller
// can clean up whatever resource, if any, its chunks were being written to;
// a completed session's onAbort is never called, since there's nothing to
// clean up — it's already a finished, correct upload.
func (st *sessionStore[T]) sweepExpired(onAbort func(name string, sess T)) {
	now := time.Now()

	st.mu.Lock()
	var abortNames []string
	var abortSessions []T
	for name, sess := range st.sessions {
		c := sess.core()
		if c.processing || now.Before(c.expiresAt) {
			continue
		}
		if !c.completed {
			abortNames = append(abortNames, name)
			abortSessions = append(abortSessions, sess)
		}
		delete(st.sessions, name)
	}
	st.mu.Unlock()

	if onAbort == nil {
		return
	}
	for i, name := range abortNames {
		onAbort(name, abortSessions[i])
	}
}
