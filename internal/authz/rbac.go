package authz

import (
	"context"
	"sync"
)

// Permission is a single capability string ("resource:action") that a role may grant.
type Permission string

const (
	// PermissionReadRoom allows viewing a room and its messages.
	PermissionReadRoom Permission = "room:read"
	// PermissionWriteRoom allows posting messages to a room.
	PermissionWriteRoom Permission = "room:write"
	// PermissionDeleteRoom allows deleting a room.
	PermissionDeleteRoom Permission = "room:delete"
	// PermissionManageRoom allows changing room settings, roles, and membership.
	PermissionManageRoom Permission = "room:manage"
	// PermissionKickUser allows removing a user from a room (they may rejoin).
	PermissionKickUser Permission = "user:kick"
	// PermissionBanUser allows permanently barring a user from a room.
	PermissionBanUser Permission = "user:ban"
	// PermissionMuteUser allows preventing a user from speaking/posting.
	PermissionMuteUser Permission = "user:mute"
)

// Role is a named bundle of Permissions assignable to a user on a resource.
type Role struct {
	Name        string
	Permissions []Permission
}

var (
	// RoleMember is the baseline role granting only read access.
	RoleMember = Role{
		Name: "member",
		Permissions: []Permission{
			PermissionReadRoom,
		},
	}

	// RoleModerator can read and write plus kick and mute users, but cannot
	// delete/manage rooms or ban users.
	RoleModerator = Role{
		Name: "moderator",
		Permissions: []Permission{
			PermissionReadRoom,
			PermissionWriteRoom,
			PermissionKickUser,
			PermissionMuteUser,
		},
	}

	// RoleAdmin holds every room and user permission.
	RoleAdmin = Role{
		Name: "admin",
		Permissions: []Permission{
			PermissionReadRoom,
			PermissionWriteRoom,
			PermissionDeleteRoom,
			PermissionManageRoom,
			PermissionKickUser,
			PermissionBanUser,
			PermissionMuteUser,
		},
	}
)

// RBAC is an in-memory role-based access control store. roles maps role name to
// its definition; userRoles maps userID -> resourceID -> role name (one role per
// user per resource). All state is held under mu and is not persisted, so it is
// lost on restart.
type RBAC struct {
	roles     map[string]Role
	userRoles map[string]map[string]string
	mu        sync.RWMutex
}

// NewRBAC returns an RBAC preloaded with the built-in member, moderator, and admin
// roles and no user assignments.
func NewRBAC() *RBAC {
	rbac := &RBAC{
		roles:     make(map[string]Role),
		userRoles: make(map[string]map[string]string),
	}

	rbac.roles[RoleMember.Name] = RoleMember
	rbac.roles[RoleModerator.Name] = RoleModerator
	rbac.roles[RoleAdmin.Name] = RoleAdmin

	return rbac
}

// AssignRole assigns roleName to the user for the given resource, replacing any
// prior role on that resource. Safe for concurrent use. It does not validate that
// roleName is a known role; an unknown role simply grants no permissions.
func (r *RBAC) AssignRole(userID, resourceID, roleName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.userRoles[userID] == nil {
		r.userRoles[userID] = make(map[string]string)
	}

	r.userRoles[userID][resourceID] = roleName
}

// HasPermission reports whether the user's role on resourceID includes permission.
// It returns false (deny by default) if the user has no role on the resource or the
// assigned role is unknown. Safe for concurrent use. ctx is currently unused.
func (r *RBAC) HasPermission(ctx context.Context, userID, resourceID string, permission Permission) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	userResourceRoles, ok := r.userRoles[userID]
	if !ok {
		return false
	}

	roleName, ok := userResourceRoles[resourceID]
	if !ok {
		return false
	}

	role, ok := r.roles[roleName]
	if !ok {
		return false
	}

	for _, p := range role.Permissions {
		if p == permission {
			return true
		}
	}

	return false
}

// GetUserRole returns the user's role name on resourceID and whether one is set.
// Safe for concurrent use.
func (r *RBAC) GetUserRole(userID, resourceID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if userResourceRoles, ok := r.userRoles[userID]; ok {
		role, exists := userResourceRoles[resourceID]
		return role, exists
	}

	return "", false
}
