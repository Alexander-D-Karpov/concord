// Package mentions parses @-mentions out of message content and resolves them to
// user IDs.
//
// Parser.Parse merges client-supplied hint IDs with database validation rather
// than relying purely on the text, so callers should pass any IDs the client has
// already resolved. It is invoked from chat.Service.SendMessage.
package mentions
