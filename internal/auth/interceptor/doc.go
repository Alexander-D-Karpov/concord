// Package interceptor provides the gRPC unary and stream interceptors that
// authenticate requests and inject the caller's identity into the context.
//
// It validates the access token via jwt.Manager and exposes the resulting
// identity through GetUserID/GetHandle/GetClaims. Two allowlists govern behavior:
// publicMethods bypass authentication entirely (login, register, refresh,
// reflection, health) and machineAuthMethods use machine auth instead. A new
// public RPC that is not added to publicMethods will be rejected with 401.
package interceptor
