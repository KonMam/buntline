// Matching a queued bubble to its real message event. The server may
// expand steering content: the steering queue carries user messages
// only, so @-mention attachment contents are inlined into the message
// server-side, which means the transcript message can start with the
// queued text rather than equal it. The expansion is exact
// ("\n\nContents of ..."), so a prefix is only accepted when the
// content continues with that expansion — a later message that merely
// shares a prefix can never steal a pending bubble.
export function queuedLanded(transcriptContent: string, queuedText: string): boolean {
  if (transcriptContent === queuedText) return true;
  return (
    transcriptContent.length > queuedText.length &&
    transcriptContent.startsWith(queuedText) &&
    transcriptContent.startsWith('\n\nContents of ', queuedText.length)
  );
}
