package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type postgresOrgStore struct {
	db *sql.DB
}

func NewPostgresOrgStore(db *sql.DB) OrgStore {
	return &postgresOrgStore{db: db}
}

const orgColumns = `id, parent_id, name, path, external_source, external_id`

func scanOrg(row interface{ Scan(...any) error }) (*Org, error) {
	var o Org
	if err := row.Scan(&o.ID, &o.ParentID, &o.Name, &o.Path,
		&o.ExternalSource, &o.ExternalID); err != nil {
		return nil, err
	}
	return &o, nil
}

// Create 写入节点并按父节点的 path 计算自己的 path。
// path 形如 /root/child/leaf，由 ID 拼成——因此改名不影响 path。
func (s *postgresOrgStore) Create(ctx context.Context, o *Org) error {
	path := "/" + o.ID
	if o.ParentID != nil {
		parent, err := s.Get(ctx, *o.ParentID)
		if err != nil {
			return fmt.Errorf("父节点不可用: %w", err)
		}
		path = parent.Path + "/" + o.ID
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO organizations (id, parent_id, name, path, external_source, external_id, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		o.ID, o.ParentID, o.Name, path, o.ExternalSource, o.ExternalID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("创建组织节点失败: %w", err)
	}
	o.Path = path
	return nil
}

func (s *postgresOrgStore) Get(ctx context.Context, id string) (*Org, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+orgColumns+` FROM organizations WHERE id = $1`, id)
	o, err := scanOrg(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOrgNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询组织节点失败: %w", err)
	}
	return o, nil
}

func (s *postgresOrgStore) Rename(ctx context.Context, id, name string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE organizations SET name = $1, updated_at = $2 WHERE id = $3`,
		name, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("重命名组织节点失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrOrgNotFound
	}
	return nil
}

func (s *postgresOrgStore) Children(ctx context.Context, parentID *string) ([]*Org, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if parentID == nil {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+orgColumns+` FROM organizations WHERE parent_id IS NULL ORDER BY name`)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+orgColumns+` FROM organizations WHERE parent_id = $1 ORDER BY name`, *parentID)
	}
	if err != nil {
		return nil, fmt.Errorf("查询子节点失败: %w", err)
	}
	defer rows.Close()
	return collectOrgs(rows)
}

// Subtree 返回该节点自身及其全部后代。
// 用 path = $1 OR path LIKE $1 || '/%' —— 加上 '/' 分隔符可以避免
// /root/rd 误吞 /root/rd2 这类同前缀兄弟节点。
func (s *postgresOrgStore) Subtree(ctx context.Context, id string) ([]*Org, error) {
	self, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+orgColumns+` FROM organizations
		 WHERE path = $1 OR path LIKE $1 || '/%'
		 ORDER BY path`, self.Path)
	if err != nil {
		return nil, fmt.Errorf("查询子树失败: %w", err)
	}
	defer rows.Close()
	return collectOrgs(rows)
}

func (s *postgresOrgStore) ByExternal(ctx context.Context, source, externalID string) (*Org, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+orgColumns+` FROM organizations
		 WHERE external_source = $1 AND external_id = $2`, source, externalID)
	o, err := scanOrg(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOrgNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("按外部 ID 查询组织节点失败: %w", err)
	}
	return o, nil
}

func (s *postgresOrgStore) All(ctx context.Context) ([]*Org, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+orgColumns+` FROM organizations ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("查询全部组织节点失败: %w", err)
	}
	defer rows.Close()
	return collectOrgs(rows)
}

func collectOrgs(rows *sql.Rows) ([]*Org, error) {
	var out []*Org
	for rows.Next() {
		o, err := scanOrg(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描组织节点失败: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Move 在 Task 14 实现。
func (s *postgresOrgStore) Move(ctx context.Context, id string, newParentID *string) error {
	return errors.New("Move 尚未实现")
}

// Delete 在 Task 15 实现。
func (s *postgresOrgStore) Delete(ctx context.Context, id string) error {
	return errors.New("Delete 尚未实现")
}
