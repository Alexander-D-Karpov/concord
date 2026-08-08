// Package oauth integrates external OAuth2 identity providers (Google, GitHub).
//
// Manager builds per-provider authorization URLs, exchanges authorization codes,
// and normalizes each provider's user-info response into a common UserInfo.
// Callers must generate a CSRF state value and verify it with ValidateState on
// callback; parseUserInfo is provider-specific because the JSON shapes differ.
package oauth
