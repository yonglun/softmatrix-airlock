package control

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/softmatrix/airlock/internal/authz"
)

type ApprovalDeps struct {
	Requests RequestStore
	Notifs   NotificationStore
	Keys     KeyStore
	Orgs     OrgStore
	RBAC     RBACStore
	Users    UserStore
	Issuer   *KeyIssuer
	Resolver *authz.Resolver
}

type ApprovalService struct {
	deps ApprovalDeps
}

func NewApprovalService(deps ApprovalDeps) *ApprovalService {
	return &ApprovalService{deps: deps}
}

// SubmitInput 是一次申请提交。两类申请共用它，各自只填自己那几个字段——
// 数据库的 requests_kind_shape_check 会兜住填错的组合。
type SubmitInput struct {
	Kind        string
	RequesterID string
	OrgID       string
	Reason      string

	KeyName string
	Models  []string

	TargetKeyID   string
	BumpToBudget  float64
	BumpExpiresAt *time.Time
}

// Submit 创建一张申请单并给审批人排通知。
//
// 目标的存在性在这里就校验，而不是等审批通过后执行时才发现——
// 让申请人当场知道填错了，比让审批人批准一张注定失败的单子好。
func (s *ApprovalService) Submit(ctx context.Context, in SubmitInput) (*Request, error) {
	if _, err := s.deps.Orgs.Get(ctx, in.OrgID); err != nil {
		return nil, err
	}

	r := &Request{
		ID: uuid.NewString(), Kind: in.Kind, Status: RequestStatusPending,
		RequesterID: in.RequesterID, OrgID: in.OrgID, Reason: in.Reason,
	}

	switch in.Kind {
	case RequestKindNewKey:
		name := in.KeyName
		r.KeyName = &name
		r.Models = orEmptyStrings(in.Models)
	case RequestKindQuotaBump:
		if _, err := s.deps.Keys.Get(ctx, in.TargetKeyID); err != nil {
			return nil, err
		}
		id, budget := in.TargetKeyID, in.BumpToBudget
		r.TargetKeyID, r.BumpToBudget, r.BumpExpiresAt = &id, &budget, in.BumpExpiresAt
	default:
		return nil, fmt.Errorf("未知的申请类型: %s", in.Kind)
	}

	if err := s.deps.Requests.Create(ctx, r); err != nil {
		return nil, err
	}
	s.notifyApprovers(ctx, r)
	return r, nil
}

// notifyApprovers 给能审批该节点的人各排一封通知。
//
// 只入队不发送：投递交给 worker 重试。通知失败绝不能让已经成功的
// 提交回滚——与 P1.3a「上游 block 失败不该让吊销失败」同一条推理。
func (s *ApprovalService) notifyApprovers(ctx context.Context, r *Request) {
	approvers, err := ApproversOf(ctx, s.deps.Orgs, s.deps.RBAC, s.deps.Users, r.OrgID)
	if err != nil {
		slog.Warn("查找审批人失败，本次不发通知", "request_id", r.ID, "err", err)
		return
	}
	for _, u := range approvers {
		s.enqueue(ctx, r, NotifyEventSubmitted, u.Email,
			"有一张待审批的申请",
			fmt.Sprintf("申请单 %s（%s）等待你的审批。\n申请理由：%s",
				r.ID, kindLabel(r.Kind), r.Reason))
	}
}

func (s *ApprovalService) enqueue(
	ctx context.Context, r *Request, event, recipient, subject, body string,
) {
	if recipient == "" {
		return
	}
	if err := s.deps.Notifs.Enqueue(ctx, &Notification{
		ID: newRequestID(), RequestID: r.ID, Event: event,
		Channel: "email", Recipient: recipient, Subject: subject, Body: body,
	}); err != nil {
		slog.Warn("排入通知失败", "request_id", r.ID, "err", err)
	}
}

func newRequestID() string { return uuid.NewString() }

func kindLabel(kind string) string {
	if kind == RequestKindQuotaBump {
		return "临时提额"
	}
	return "新密钥"
}

// Approve 批准一张申请单。
//
// 只写数据库，不调上游。若在这里同步执行而上游正好宕机，审批人会拿到
// 502、这次审批等于没发生——与 P1.3a「先落库 pending 再调上游」同源。
// 真正的执行交给会重试的 worker（提额），或由申请人自助领取（新密钥）。
func (s *ApprovalService) Approve(ctx context.Context, id, deciderID string) error {
	return s.decide(ctx, id, deciderID, RequestStatusApproved,
		NotifyEventApproved, "你的申请已通过")
}

// Reject 驳回一张申请单。
func (s *ApprovalService) Reject(ctx context.Context, id, deciderID string) error {
	return s.decide(ctx, id, deciderID, RequestStatusRejected,
		NotifyEventRejected, "你的申请被驳回")
}

func (s *ApprovalService) decide(
	ctx context.Context, id, deciderID, status, event, subject string,
) error {
	r, err := s.deps.Requests.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.requireKeyWrite(ctx, deciderID, r.OrgID); err != nil {
		return err
	}
	if err := s.deps.Requests.Decide(ctx, id, status, deciderID); err != nil {
		return err
	}

	if u, err := s.deps.Users.ByID(ctx, r.RequesterID); err == nil {
		s.enqueue(ctx, r, event, u.Email, subject,
			fmt.Sprintf("申请单 %s（%s）的处理结果：%s。", r.ID, kindLabel(r.Kind), subject))
	}
	return nil
}

// requireKeyWrite 判定某人能否在该节点上审批。
//
// 权限判定下沉到服务层而不是中间件：审批路径里只有 request ID，
// 中间件拿不到它归属的节点——与 DELETE /api/keys/{id} 是同一类例外。
func (s *ApprovalService) requireKeyWrite(ctx context.Context, userID, orgID string) error {
	u, err := s.deps.Users.ByID(ctx, userID)
	if err != nil {
		return err
	}
	allowed, err := s.deps.Resolver.Can(ctx, subjectOf(u), authz.PermKeyWrite, &orgID)
	if err != nil {
		return fmt.Errorf("权限判定失败: %w", err)
	}
	if !allowed {
		return ErrPermissionDenied
	}
	return nil
}

// Claim 让申请人领取一张已批准的新密钥申请，返回 ak- 明文（仅此一次）。
//
// 为什么不由 worker 后台签发：P1.3a 定死了明文只在签发响应里返回一次、
// 库里只存哈希。后台签发就没有响应可以承载这个明文，只能加一套「加密
// 暂存、取一次后清除」的托管机制——那等于让明文落盘。让申请人自己来领，
// 明文就走 P1.3a 已验证的同步路径，从不落盘也不进邮件。
func (s *ApprovalService) Claim(
	ctx context.Context, id, requesterID string,
) (string, *APIKey, error) {
	r, err := s.deps.Requests.Get(ctx, id)
	if err != nil {
		return "", nil, err
	}
	if r.RequesterID != requesterID {
		return "", nil, ErrNotRequester
	}
	if r.Kind != RequestKindNewKey {
		return "", nil, fmt.Errorf("只有新密钥申请可以领取")
	}
	if r.Status != RequestStatusApproved {
		return "", nil, ErrRequestNotApproved
	}

	name := ""
	if r.KeyName != nil {
		name = *r.KeyName
	}
	plaintext, k, err := s.deps.Issuer.Issue(ctx, IssueRequest{
		OrgID: r.OrgID, UserID: r.RequesterID, Name: name, Models: r.Models,
	})
	if err != nil {
		// 停在 approved：转 executed 会让申请人再也领不到这把密钥。
		return "", nil, fmt.Errorf("签发失败，请稍后重试: %w", err)
	}

	// MarkExecuted 带 status='approved' 守卫。输掉这次转换意味着有另一次
	// 并发领取已经拿走了这张单子——那把刚签发的密钥不该留给任何人，
	// 立刻吊销掉，否则一次审批就换来了两把能用的密钥。
	if err := s.deps.Requests.MarkExecuted(ctx, r.ID, &k.ID, nil); err != nil {
		if rerr := s.deps.Issuer.Revoke(ctx, k.ID); rerr != nil {
			slog.Error("领取失败后吊销刚签发的密钥失败，请人工处理",
				"request_id", r.ID, "key_id", k.ID, "err", rerr)
		}
		return "", nil, err
	}
	return plaintext, k, nil
}
