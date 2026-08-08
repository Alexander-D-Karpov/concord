package push

import "context"

// noopSender is a Sender that delivers nothing; used when push is disabled/unconfigured.
type noopSender struct{}

// NewNoopSender returns a Sender that drops all messages and reports no invalid tokens.
func NewNoopSender() Sender { return noopSender{} }

func (noopSender) Send(context.Context, []Message) ([]string, error) { return nil, nil }
