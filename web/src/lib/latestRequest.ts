// latestRequest is a tiny request-sequence guard for async loads that
// outlive the UI state change that started them (a session switch, a
// module toggle). Each load bumps the sequence and captures the new
// value; a completion that no longer holds the latest value is stale and
// must not overwrite state: a rapid switch between sessions would
// otherwise let a slow response from the earlier session land last.
export function latestRequest(): { seq: () => number; current: (s: number) => boolean } {
  let seq = 0;
  return {
    // seq starts a new request and returns its sequence number.
    seq: () => ++seq,
    // current reports whether the given sequence is still the latest.
    current: (s: number) => s === seq,
  };
}
