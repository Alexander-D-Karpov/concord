package authz

import (
	"context"
	"sync"
)

type Permission string

const (
	PermissionReadRoom   Permission = "room:read"
	PermissionWriteRoom  Permission = "room:write"
	PermissionDeleteRoom Permission = "room:delete"
	PermissionManageRoom Permission = "room:manage"
	PermissionKickUser   Permission = "user:kick"
	PermissionBanUser    Permission = "user:ban"
	PermissionMuteUser   Permission = "user:mute"
)

type Role struct {
	Name        string
	Permissions []Permission
}

var (
	RoleMember = Role{
		Name: "member",
		Permissions: []Permission{
			PermissionReadRoom,
		},
	}

	RoleModerator = Role{
		Name: "moderator",
		Permissions: []Permission{
			PermissionReadRoom,
			PermissionWriteRoom,
			PermissionKickUser,
			PermissionMuteUser,
		},
	}

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

type RBAC struct {
	roles     map[string]Role
	userRoles map[string]map[string]string
	mu        sync.RWMutex
}

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

func (r *RBAC) AssignRole(userID, resourceID, roleName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.userRoles[userID] == nil {
		r.userRoles[userID] = make(map[string]string)
	}

	r.userRoles[userID][resourceID] = roleName
}

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

func (r *RBAC) GetUserRole(userID, resourceID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if userResourceRoles, ok := r.userRoles[userID]; ok {
		role, exists := userResourceRoles[resourceID]
		return role, exists
	}

	return "", false
}
