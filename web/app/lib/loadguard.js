// A request guard for an async read whose result may be stale by the time it
// resolves. It does two things:
//   - serializes same-context refreshes (an auto-refresh tick is skipped while
//     a request for the current context is still in flight), and
//   - drops a response whose context changed while it was in flight (the user
//     navigated to another directory, or the database changed).
//
// "Context" is whatever makes a response valid: a directory root's current
// path, or the clipboard's current database. bump() marks a context change;
// start() claims the slot (or returns null to skip); fresh(token) reports
// whether the context still matches; done(token) releases the slot.
export function makeRequestGuard() {
  var context = 0; // incremented on every context change
  var running = -1; // the context of the in-flight request, or -1 if none
  return {
    bump() { context += 1; },
    start() {
      if (running === context) return null;
      running = context;
      return context;
    },
    fresh(token) { return token === context; },
    done(token) { if (running === token) running = -1; },
  };
}
