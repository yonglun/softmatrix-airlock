// Package control 实现 Airlock 管理面：OIDC 登录、组织树、成员与离职对账。
// 本包不 import internal/edge——管理面与数据面在代码层面完全隔离。
package control

import (
	"context"
	"errors"
	"time"

	"github.com/softmatrix/airlock/internal/litellm"
)

const (
	UserStatusActive   = "active"
	UserStatusDisabled = "disabled"
)

// User 是 Airlock 侧的用户。身份凭证由 IdP 持有，
// 这里只镜像展示所需的最小画像 + Airlock 自己的归属与状态。
type User struct {
	ID           string
	ExternalID   string // OIDC sub
	Email        string
	DisplayName  string
	Status       string
	PrimaryOrgID *string
	LastLoginAt  *time.Time
	ReconciledAt *time.Time
}

// Session 是一次登录会话。ID 是 token 的 sha256，不是 token 本身。
type Session struct {
	ID         string
	UserID     string
	ExpiresAt  time.Time
	LastSeenAt time.Time
	IP         string
	UserAgent  string
}

// LoginState 承载授权码流程中 /login 与 /callback 之间的一次性状态。
type LoginState struct {
	ID           string
	State        string
	PKCEVerifier string
	RedirectTo   string
	ExpiresAt    time.Time
}

// Org 是组织树上的一个节点。Path 由 ID 拼成（形如 /root/child/leaf），
// 因此改名不影响 Path。
type Org struct {
	ID             string
	ParentID       *string
	Name           string
	Path           string
	ExternalSource *string
	ExternalID     *string
	// IsKeyHolder 标记该节点是密钥与预算边界，决定它是否映射为 LiteLLM Team。
	IsKeyHolder bool
}

// Identity 是从 IdP 换回来的身份信息。
type Identity struct {
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
}

var (
	ErrUserNotFound       = errors.New("用户不存在")
	ErrSessionNotFound    = errors.New("会话不存在或已过期")
	ErrLoginStateNotFound = errors.New("登录状态不存在或已过期")
	ErrOrgNotFound        = errors.New("组织节点不存在")
	ErrOrgHasChildren     = errors.New("组织节点下还有子节点")
	ErrOrgHasKeys         = errors.New("组织节点下还有密钥")
	ErrOrgCycle           = errors.New("不能把节点移动到自己的子树下")
	ErrOrgHasUsers        = errors.New("组织节点下还有归属用户")
	ErrRoleNotFound       = errors.New("角色不存在")
	ErrGrantNotFound      = errors.New("角色授予不存在")
	ErrAPIKeyNotFound     = errors.New("密钥不存在")
	ErrOrgNotKeyHolder    = errors.New("该节点不是密钥边界")

	ErrRequestNotFound    = errors.New("申请单不存在")
	ErrRequestNotPending  = errors.New("申请单不处于待审批状态")
	ErrRequestNotApproved = errors.New("申请单未处于已批准状态")
	ErrNotRequester       = errors.New("只有申请人本人可以领取")
	ErrPermissionDenied   = errors.New("没有执行该操作的权限")
)

type UserStore interface {
	ByID(ctx context.Context, id string) (*User, error)
	ByExternalID(ctx context.Context, externalID string) (*User, error)
	Upsert(ctx context.Context, u *User) (*User, error)
	ListActive(ctx context.Context) ([]*User, error)
	MarkDisabled(ctx context.Context, userIDs []string) error
	AssignPrimaryOrg(ctx context.Context, userID string, orgID *string) error
	CountByPrimaryOrg(ctx context.Context, orgID string) (int, error)
}

type SessionStore interface {
	Create(ctx context.Context, s Session) error
	Get(ctx context.Context, id string) (*Session, error)
	Touch(ctx context.Context, id string, at time.Time) error
	Delete(ctx context.Context, id string) error
	DeleteByUser(ctx context.Context, userID string) (int64, error)
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

type LoginStateStore interface {
	Create(ctx context.Context, ls LoginState) error
	// Take 取出并立即删除——登录状态是一次性的，重放必须失败。
	Take(ctx context.Context, id string) (*LoginState, error)
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

type OrgStore interface {
	Create(ctx context.Context, o *Org) error
	Get(ctx context.Context, id string) (*Org, error)
	Rename(ctx context.Context, id, name string) error
	Move(ctx context.Context, id string, newParentID *string) error
	Delete(ctx context.Context, id string) error
	SetKeyHolder(ctx context.Context, id string, v bool) error
	// CountLiveKeys 统计该节点下 active 与 pending 的密钥数。
	CountLiveKeys(ctx context.Context, orgID string) (int, error)
	Children(ctx context.Context, parentID *string) ([]*Org, error)
	Subtree(ctx context.Context, id string) ([]*Org, error)
	ByExternal(ctx context.Context, source, externalID string) (*Org, error)
	All(ctx context.Context) ([]*Org, error)
}

// RoleGrant 是一条角色授予的完整记录，用于展示。
type RoleGrant struct {
	ID        string
	UserID    string
	RoleID    string
	OrgID     *string
	GrantedBy *string
	CreatedAt time.Time
}

// Role 是一个角色。权限集不在这里——它由 authz 包的注册表定义。
type Role struct {
	ID          string
	Name        string
	Description string
	IsBuiltin   bool
}

// RBACStore 管理角色与授予，并为 authz.Resolver 提供判定数据。
type RBACStore interface {
	// SyncBuiltinRoles 把 Go 侧定义的内置角色与其权限集写入数据库。
	// 幂等：每次启动都跑，保证数据库与代码一致。
	SyncBuiltinRoles(ctx context.Context) error
	// ValidatePermissions 校验数据库里的权限字符串都已在 Go 注册表中注册。
	ValidatePermissions(ctx context.Context) error

	ListRoles(ctx context.Context) ([]Role, error)
	CreateGrant(ctx context.Context, g RoleGrant) error
	GetGrant(ctx context.Context, id string) (RoleGrant, error)
	DeleteGrant(ctx context.Context, id string) error
	ListGrantsForUser(ctx context.Context, userID string) ([]RoleGrant, error)
	ListGrantsForOrg(ctx context.Context, orgID string) ([]RoleGrant, error)
	CountGlobalGrantsOfRole(ctx context.Context, roleID string) (int, error)
}

// APIKey 是控制面视角的一把虚拟密钥。UpstreamKeyEnc 是加密后的上游密钥。
type APIKey struct {
	ID             string
	KeyHash        string
	KeyPrefix      string
	OrgID          string
	UserID         string
	Name           string
	UpstreamKeyEnc string
	Status         string
	Models         []string
	MaxBudget      *float64
	BudgetDuration *string
	RPMLimit       *int
	TPMLimit       *int
	ExpiresAt      *time.Time
	CreatedAt      time.Time
}

type KeyStore interface {
	CreatePending(ctx context.Context, k *APIKey) error
	MarkActive(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (*APIKey, error)
	ListByOrg(ctx context.Context, orgID string) ([]*APIKey, error)
	Revoke(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	// ListStalePending 返回滞留超过 olderThan 的 pending 密钥。
	ListStalePending(ctx context.Context, olderThan time.Duration) ([]*APIKey, error)
}

// LiteLLMKeyAdmin 是签发所需的上游密钥能力。
// 定义在 control 侧、由 internal/litellm 实现——依赖方向单向。
type LiteLLMKeyAdmin interface {
	GenerateKey(ctx context.Context, k litellm.Key) error
	KeyExists(ctx context.Context, key string) (bool, error)
	BlockKey(ctx context.Context, key string) error
	DeleteKey(ctx context.Context, key string) error
}

// 申请单的 kind 与 status 取值。与迁移里的 CHECK 约束一一对应。
const (
	RequestKindNewKey    = "new_key"
	RequestKindQuotaBump = "quota_bump"

	RequestStatusPending  = "pending"
	RequestStatusRejected = "rejected"
	RequestStatusApproved = "approved"
	RequestStatusExecuted = "executed"
	RequestStatusFailed   = "failed"
)

// 通知事件。与 notifications.event 的 CHECK 约束一一对应。
const (
	NotifyEventSubmitted = "submitted"
	NotifyEventApproved  = "approved"
	NotifyEventRejected  = "rejected"
	NotifyEventExecuted  = "executed"
	NotifyEventReclaimed = "reclaimed"
)

// Request 是一张申请单。它同时就是这条流程的审计轨迹：
// 谁申请、谁审批、何时、执行结果、何时回收，全在这一行里。
type Request struct {
	ID          string
	Kind        string
	Status      string
	RequesterID string
	OrgID       string
	Reason      string

	KeyName *string
	Models  []string

	TargetKeyID   *string
	BumpToBudget  *float64
	BumpExpiresAt *time.Time
	PrevBudget    *float64
	ReclaimedAt   *time.Time

	DecidedBy   *string
	DecidedAt   *time.Time
	ExecutedAt  *time.Time
	IssuedKeyID *string
	LastError   *string
	Attempts    int
	CreatedAt   time.Time
}

type RequestStore interface {
	Create(ctx context.Context, r *Request) error
	Get(ctx context.Context, id string) (*Request, error)
	ListByRequester(ctx context.Context, userID string) ([]*Request, error)
	// Decide 只在 pending 时生效，否则返回 ErrRequestNotPending。
	Decide(ctx context.Context, id, status, decidedBy string) error
	// MarkExecuted 只在 approved 时生效，否则返回 ErrRequestNotApproved。
	MarkExecuted(ctx context.Context, id string, issuedKeyID *string, prevBudget *float64) error
	MarkFailed(ctx context.Context, id, reason string) error
	RecordAttempt(ctx context.Context, id, lastErr string) error
	ListApprovedBumps(ctx context.Context) ([]*Request, error)
	ListExpiredBumps(ctx context.Context, now time.Time) ([]*Request, error)
	MarkReclaimed(ctx context.Context, id string) error
}

// Notification 是 outbox 里的一条待投递通知。
type Notification struct {
	ID        string
	RequestID string
	Event     string
	Channel   string
	Recipient string
	Subject   string
	Body      string
	Status    string
	Attempts  int
	LastError *string
	SentAt    *time.Time
	CreatedAt time.Time
}

type NotificationStore interface {
	Enqueue(ctx context.Context, n *Notification) error
	ListPending(ctx context.Context, limit int) ([]*Notification, error)
	MarkSent(ctx context.Context, id string) error
	// RecordFailure 计一次失败但保持 pending，下一轮继续重试。
	RecordFailure(ctx context.Context, id, lastErr string) error
	// MarkFailed 终止重试，留待人工查看。
	MarkFailed(ctx context.Context, id, lastErr string) error
}
