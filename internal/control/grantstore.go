package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/softmatrix/airlock/internal/authz"
)

type postgresRBACStore struct {
	db *sql.DB
}

// NewPostgresRBACStore 返回 RBAC 存储。
// 返回具体类型而非接口，因为它同时实现 RBACStore 与 authz.Store 两套契约。
func NewPostgresRBACStore(db *sql.DB) *postgresRBACStore {
	return &postgresRBACStore{db: db}
}

// SyncBuiltinRoles 把 Go 侧定义的内置角色与权限集写入数据库。
//
// 权限集的唯一真相来源是 authz 包的注册表，不是数据库——因此每次启动都整体
// 覆盖内置角色的权限行（先删后插），保证代码里删掉的权限不会以脏数据形式留存。
// 自定义角色（is_builtin = false）完全不受影响。
func (s *postgresRBACStore) SyncBuiltinRoles(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, r := range authz.BuiltinRoles() {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO roles (id, name, description, is_builtin)
			VALUES ($1, $2, $3, true)
			ON CONFLICT (id) DO UPDATE SET
				name = EXCLUDED.name,
				description = EXCLUDED.description,
				is_builtin = true`,
			r.ID, r.Name, r.Description); err != nil {
			return fmt.Errorf("写入角色失败（%s）: %w", r.ID, err)
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM role_permissions WHERE role_id = $1`, r.ID); err != nil {
			return fmt.Errorf("清理角色权限失败（%s）: %w", r.ID, err)
		}
		for _, p := range r.Permissions {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2)`,
				r.ID, p); err != nil {
				return fmt.Errorf("写入角色权限失败（%s/%s）: %w", r.ID, p, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交角色同步事务失败: %w", err)
	}
	return nil
}

// ValidatePermissions 校验数据库里的权限字符串都已在 Go 注册表中注册。
//
// 未注册的权限在判定时会被静默忽略——该角色因此悄悄少了一项能力，
// 且没有任何报错。宁可启动失败，也不要带着这种故障运行。
func (s *postgresRBACStore) ValidatePermissions(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT permission FROM role_permissions ORDER BY permission`)
	if err != nil {
		return fmt.Errorf("查询权限清单失败: %w", err)
	}
	defer rows.Close()

	var unknown []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return fmt.Errorf("扫描权限行失败: %w", err)
		}
		if !authz.IsKnown(p) {
			unknown = append(unknown, p)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(unknown) > 0 {
		return fmt.Errorf(
			"数据库里存在未注册的权限 %s；"+
				"这通常是迁移写错或版本回退留下的脏数据，"+
				"会让相关角色静默少一项能力，请先清理后再启动",
			strings.Join(unknown, ", "))
	}
	return nil
}

func (s *postgresRBACStore) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, is_builtin FROM roles ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("查询角色失败: %w", err)
	}
	defer rows.Close()

	var out []Role
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.IsBuiltin); err != nil {
			return nil, fmt.Errorf("扫描角色行失败: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *postgresRBACStore) CreateGrant(ctx context.Context, g RoleGrant) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO role_grants (id, user_id, role_id, org_id, granted_by)
		VALUES ($1, $2, $3, $4, $5)`,
		g.ID, g.UserID, g.RoleID, g.OrgID, g.GrantedBy)
	if err != nil {
		return fmt.Errorf("创建角色授予失败: %w", err)
	}
	return nil
}

func (s *postgresRBACStore) GetGrant(ctx context.Context, id string) (RoleGrant, error) {
	var g RoleGrant
	err := s.db.QueryRowContext(ctx,
		`SELECT `+grantColumns+` FROM role_grants WHERE id = $1`, id).
		Scan(&g.ID, &g.UserID, &g.RoleID, &g.OrgID, &g.GrantedBy, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RoleGrant{}, ErrGrantNotFound
	}
	if err != nil {
		return RoleGrant{}, fmt.Errorf("查询角色授予失败: %w", err)
	}
	return g, nil
}

func (s *postgresRBACStore) DeleteGrant(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM role_grants WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("删除角色授予失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrGrantNotFound
	}
	return nil
}

const grantColumns = `id, user_id, role_id, org_id, granted_by, created_at`

func scanGrants(rows *sql.Rows) ([]RoleGrant, error) {
	var out []RoleGrant
	for rows.Next() {
		var g RoleGrant
		if err := rows.Scan(&g.ID, &g.UserID, &g.RoleID, &g.OrgID,
			&g.GrantedBy, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("扫描授予行失败: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *postgresRBACStore) ListGrantsForUser(ctx context.Context, userID string) ([]RoleGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+grantColumns+` FROM role_grants WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("按用户查询授予失败: %w", err)
	}
	defer rows.Close()
	return scanGrants(rows)
}

func (s *postgresRBACStore) ListGrantsForOrg(ctx context.Context, orgID string) ([]RoleGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+grantColumns+` FROM role_grants WHERE org_id = $1 ORDER BY created_at`, orgID)
	if err != nil {
		return nil, fmt.Errorf("按组织查询授予失败: %w", err)
	}
	defer rows.Close()
	return scanGrants(rows)
}

// CountGlobalGrantsOfRole 只数全局授予。
// CheckBootstrapConfig 用它判断「系统里有没有管理员」——
// 节点级的平台管理员授予不算，因为它拿不到全局能力。
func (s *postgresRBACStore) CountGlobalGrantsOfRole(ctx context.Context, roleID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM role_grants rg
		JOIN users u ON u.id = rg.user_id
		WHERE rg.role_id = $1 AND rg.org_id IS NULL AND u.status = $2`,
		roleID, UserStatusActive).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("统计全局角色授予失败: %w", err)
	}
	return n, nil
}

// ---- authz.Store 实现 ----

func (s *postgresRBACStore) GrantsForUser(ctx context.Context, userID string) ([]authz.Grant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT role_id, org_id FROM role_grants WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("查询用户授予失败: %w", err)
	}
	defer rows.Close()

	var out []authz.Grant
	for rows.Next() {
		var g authz.Grant
		if err := rows.Scan(&g.RoleID, &g.OrgID); err != nil {
			return nil, fmt.Errorf("扫描授予行失败: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *postgresRBACStore) PermissionsForRole(ctx context.Context, roleID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT permission FROM role_permissions WHERE role_id = $1`, roleID)
	if err != nil {
		return nil, fmt.Errorf("查询角色权限失败: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("扫描权限行失败: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(out) // 顺序稳定，便于测试与展示
	return out, nil
}

func (s *postgresRBACStore) OrgPath(ctx context.Context, orgID string) (string, error) {
	var path string
	err := s.db.QueryRowContext(ctx,
		`SELECT path FROM organizations WHERE id = $1`, orgID).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", authz.ErrOrgNotFound
	}
	if err != nil {
		return "", fmt.Errorf("查询节点路径失败: %w", err)
	}
	return path, nil
}
