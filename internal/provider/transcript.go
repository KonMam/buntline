package provider

import "slices"

// RepairToolPairing enforces the one structural rule every
// OpenAI-compatible backend checks before it will even read a request:
// every tool message answers exactly one tool_call from the assistant
// message that opened it, and every tool_call gets exactly one answer.
//
// Breaking it is not a soft failure. The backend rejects the whole
// request ("messages with role 'tool' must be a response to a preceding
// message with 'tool_calls'"), and because the transcript replays on
// every later turn, one malformed message bricks the session for good
// instead of costing a single reply. That is worth a guard even when the
// loop is believed correct: the cost of a bug here is the session, not
// the turn.
//
// Repairs never drop model or tool output. An unmatched tool result
// keeps its text as a user message; a tool_call the transcript never
// answered gets a stub. Every repair returns a note, because a repair
// means something upstream built an invalid transcript, and fixing that
// silently would turn a loud bug into a quiet one.
func RepairToolPairing(msgs []Message) ([]Message, []string) {
	var (
		out   []Message
		notes []string
		open  []string // tool_call ids opened by the last assistant message, still unanswered
	)

	// closeOpen stubs out every tool_call left unanswered, so a block is
	// complete before anything that is not its own tool result follows.
	closeOpen := func() {
		for _, id := range open {
			out = append(out, Message{
				Role:       RoleTool,
				Content:    "[no result recorded for this tool call]",
				ToolCallID: id,
			})
			notes = append(notes, "tool call "+id+" was never answered; inserted a stub result")
		}
		open = nil
	}

	for _, m := range msgs {
		switch {
		case m.Role == RoleTool:
			// Answers may arrive in any order within their block, so match
			// on the id rather than on position.
			if i := slices.Index(open, m.ToolCallID); i >= 0 {
				open = slices.Delete(open, i, i+1)
				out = append(out, m)
				continue
			}
			// No open call answers this id: it is a duplicate answer, or
			// it belongs to a call that is no longer in the transcript.
			// Keep the content, drop the tool framing.
			closeOpen()
			out = append(out, Message{
				Role:    RoleUser,
				Kind:    "tool_result",
				Content: "[tool result]\n" + m.Content,
			})
			notes = append(notes, "tool result for "+m.ToolCallID+" answered no open call; kept as a user message")
		case len(m.ToolCalls) > 0:
			closeOpen()
			out = append(out, m)
			for _, c := range m.ToolCalls {
				open = append(open, c.ID)
			}
		default:
			closeOpen()
			out = append(out, m)
		}
	}
	closeOpen()

	if len(notes) == 0 {
		return msgs, nil
	}
	return out, notes
}
