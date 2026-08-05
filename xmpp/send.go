package xmpp

import (
	"context"
	"fmt"

	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/stanza"
)

// Send sends a chat-message stanza with the given body to "to", returning
// the stanza ID it was sent with (needed to later correct or be replied to).
func (c *Client) Send(ctx context.Context, to, body string, opts SendOptions) (string, error) {
	toJID, err := jid.Parse(to)
	if err != nil {
		return "", fmt.Errorf("parsing recipient %q: %w", to, err)
	}
	id := randomID()
	msg := messageBody{
		Message: stanza.Message{
			To:   toJID,
			Type: stanza.ChatMessage,
			ID:   id,
		},
		Body: body,
	}
	switch {
	case opts.Encrypted != nil:
		msg.Encrypted = opts.Encrypted
		msg.Body = "This message is encrypted with OMEMO v2 (XEP-0384)" // fallback for non-OMEMO clients
		// For encrypted replies, also send the <reply/> element (IDs only, unencrypted)
		if opts.ReplyToID != "" {
			msg.Reply = &replyElem{To: to, ID: opts.ReplyToID}
		}
	case opts.EncryptedV1 != nil:
		msg.EncryptedV1 = opts.EncryptedV1
		msg.Body = "This message is encrypted with legacy OMEMO" // fallback for non-OMEMO clients
		if opts.ReplyToID != "" {
			msg.Reply = &replyElem{To: to, ID: opts.ReplyToID}
		}
	case opts.ReactionTargetID != "":
		msg.Reactions = &reactionsElem{ID: opts.ReactionTargetID, Reactions: opts.Reactions}
	case opts.RetractID != "":
		msg.Retract = &retractElem{ID: opts.RetractID}
		msg.Body = retractFallbackBody
		msg.Fallback = &fallbackElem{
			For:  "urn:xmpp:message-retract:1",
			Body: &fallbackBodyElem{}, // no start/end: the whole body is fallback text
		}
	case opts.ReplaceID != "":
		msg.Replace = &replaceElem{ID: opts.ReplaceID}
	case opts.ReplyToID != "":
		quote := BuildFallbackQuote(opts.QuotedAuthor, opts.QuotedBody)
		end := len(quote)
		msg.Body = quote + body
		msg.Reply = &replyElem{To: to, ID: opts.ReplyToID}
		msg.Fallback = &fallbackElem{
			For:  "urn:xmpp:reply:0",
			Body: &fallbackBodyElem{End: &end},
		}
	}
	if err := c.session.Encode(ctx, msg); err != nil {
		return "", err
	}
	return id, nil
}

// SendChatState sends a standalone XEP-0085 chat state notification to "to"
// — no body, just the state. Typically sent as the user starts typing
// (ChatStateComposing) and again once they stop without sending
// (ChatStateActive) or send it another way.
func (c *Client) SendChatState(ctx context.Context, to string, state ChatState) error {
	toJID, err := jid.Parse(to)
	if err != nil {
		return fmt.Errorf("parsing recipient %q: %w", to, err)
	}
	msg := messageBody{Message: stanza.Message{To: toJID, Type: stanza.ChatMessage, ID: randomID()}}
	msg.setChatState(state)
	return c.session.Encode(ctx, msg)
}
