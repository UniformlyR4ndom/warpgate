// Clipboard auto-refresh loop: long polls (the server holds the request until
// a change or its own timeout), never starting two polls within MIN_GAP_MS.
// A failed long poll is blamed on long polling only if a plain poll right
// after it succeeds; then the loop polls plainly and retries after RETRY_MS.
export const MIN_GAP_MS = 1000;
export const RETRY_MS = 5 * 60 * 1000;
const BACKOFF_MS = [1000, 2000, 4000, 10000];

// poll({wait, signal}) resolves to {status: 'ok'|'skipped'} or
// {status: 'error', error, suspect}; suspect marks a failure long polling may
// have caused.
export function makeClipPoller(deps) {
  var poll = deps.poll;
  var now = deps.now || Date.now;
  var sleep = deps.sleep || function(ms) { return new Promise(function(r) { setTimeout(r, ms); }); };
  var log = deps.log || console;

  var gen = 0;
  var active = false;
  var ctrl = null;
  var lastStart = -Infinity;
  var failures = 0;
  var mode = 'long';
  var retryAt = 0;
  var suspect = null; // {error, elapsed} of a long poll awaiting its plain check

  async function run(myGen) {
    while (gen === myGen) {
      var gap = failures ? BACKOFF_MS[Math.min(failures, BACKOFF_MS.length) - 1] : MIN_GAP_MS;
      var delay = lastStart + gap - now();
      if (delay > 0) {
        await sleep(delay);
        if (gen !== myGen) return;
      }
      var wait = !suspect && (mode === 'long' || now() >= retryAt);
      ctrl = new AbortController();
      lastStart = now();
      var r = await poll({ wait: wait, signal: ctrl.signal });
      var elapsed = now() - lastStart;
      if (gen !== myGen) return;
      if (r.status === 'skipped') continue;
      if (r.status === 'ok') {
        failures = 0;
        if (suspect) {
          log.warn('warpgate: clipboard long polling failed (' + suspect.error + ' after ' +
            (suspect.elapsed / 1000).toFixed(1) + ' s), plain polls work; falling back to 1 s polling, retrying in 5 min');
          mode = 'short';
          retryAt = now() + RETRY_MS;
          suspect = null;
        } else if (wait && mode === 'short') {
          mode = 'long';
          log.info('warpgate: clipboard long polling works again');
        }
        continue;
      }
      failures++;
      suspect = wait && r.suspect ? { error: r.error, elapsed: elapsed } : null;
    }
  }

  return {
    start() {
      if (active) return;
      active = true;
      run(++gen);
    },
    stop() {
      if (!active) return;
      active = false;
      gen++;
      if (ctrl) ctrl.abort();
    },
    // Cancels the poll in flight; a running loop goes on with its next one.
    abort() { if (ctrl) ctrl.abort(); },
    get mode() { return mode; },
  };
}
