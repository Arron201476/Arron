package identity

import (
	"context"
	"strings"
)

const (
	DefaultWorkspaceID   = "workspace_internal_shared"
	DefaultUserID        = "user_local_default"
	DefaultWorkspaceName = "本地工作区"
	DefaultUserName      = "本地用户"
)

type Kind string

const (
	KindUser    Kind = "user"
	KindService Kind = "service"
	KindSystem  Kind = "system"
)

type Role string

const (
	RoleViewer Role = "viewer"
	RoleEditor Role = "editor"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

type Principal struct {
	Kind          Kind   `json:"kind"`
	UserID        string `json:"user_id,omitempty"`
	DisplayName   string `json:"display_name,omitempty"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	Role          Role   `json:"role,omitempty"`
	AuthMethod    string `json:"auth_method,omitempty"`
}

func DefaultLocalPrincipal() Principal {
	return Principal{
		Kind: KindUser, UserID: DefaultUserID, DisplayName: DefaultUserName,
		WorkspaceID: DefaultWorkspaceID, WorkspaceName: DefaultWorkspaceName,
		Role: RoleOwner, AuthMethod: "local_loopback",
	}
}

func ServicePrincipal() Principal {
	return Principal{Kind: KindService, DisplayName: "Agent Sidecar", AuthMethod: "service_token"}
}

func SystemPrincipal() Principal {
	return Principal{Kind: KindSystem, DisplayName: "Runtime", AuthMethod: "system"}
}

func (p Principal) ValidUser() bool {
	return p.Kind == KindUser && strings.TrimSpace(p.UserID) != "" &&
		strings.TrimSpace(p.WorkspaceID) != "" && ValidRole(p.Role)
}

func (p Principal) ActorRef() string {
	switch p.Kind {
	case KindUser:
		if strings.TrimSpace(p.UserID) != "" {
			return p.UserID
		}
	case KindService:
		return "service_agent_sidecar"
	}
	return "system_runtime"
}

func ValidRole(role Role) bool {
	_, ok := roleRank[role]
	return ok
}

func (p Principal) Allows(required Role) bool {
	if p.Kind == KindService || p.Kind == KindSystem {
		return true
	}
	actual, ok := roleRank[p.Role]
	if !ok {
		return false
	}
	minimum, ok := roleRank[required]
	return ok && actual >= minimum
}

var roleRank = map[Role]int{
	RoleViewer: 1,
	RoleEditor: 2,
	RoleAdmin:  3,
	RoleOwner:  4,
}

type principalContextKey struct{}
type delegatedUserContextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func FromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

func UserFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := FromContext(ctx)
	if ok && principal.Kind == KindService {
		delegated, present := ctx.Value(delegatedUserContextKey{}).(Principal)
		return delegated, present && delegated.ValidUser()
	}
	return principal, ok && principal.Kind == KindUser && principal.ValidUser()
}

// WithDelegatedUser keeps service authentication separate from the persisted
// task's effective user. Callers must resolve the user from trusted runtime data.
func WithDelegatedUser(ctx context.Context, user Principal) context.Context {
	return context.WithValue(ctx, delegatedUserContextKey{}, user)
}

func WorkspaceIDFromContext(ctx context.Context) string {
	if principal, ok := UserFromContext(ctx); ok {
		return principal.WorkspaceID
	}
	return DefaultWorkspaceID
}

func UserIDFromContext(ctx context.Context) string {
	if principal, ok := UserFromContext(ctx); ok {
		return principal.UserID
	}
	return DefaultUserID
}

func ActorRefFromContext(ctx context.Context) string {
	if principal, ok := FromContext(ctx); ok {
		return principal.ActorRef()
	}
	return "system_runtime"
}
