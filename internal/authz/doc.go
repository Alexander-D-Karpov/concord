// Package authz provides role-based access control (member, moderator, admin)
// with a cache-backed permission-check layer.
//
// RBAC holds roles and per-user/per-resource assignments in memory under an
// RWMutex; PermissionCache memoizes HasPermission results with a TTL. Role
// assignments are not persisted (in-memory only). Call InvalidateUser after a role
// change to drop that user's cached decisions (it uses a SCAN-based pattern delete,
// matching the per-resource key layout); otherwise a decision clears on TTL expiry.
package authz
