-- +goose Up
CREATE TABLE roles (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    is_builtin  BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE role_permissions (
    role_id    TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission TEXT NOT NULL,
    PRIMARY KEY (role_id, permission)
);

CREATE TABLE role_grants (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id    TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
    -- org_id 为 NULL 表示全局授予
    org_id     TEXT REFERENCES organizations(id) ON DELETE CASCADE,
    granted_by TEXT REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 普通唯一索引挡不住重复的全局授予：Postgres 认为 NULL 彼此不相等，
-- (user_id, role_id, NULL) 能被插入任意多次。必须用表达式索引。
CREATE UNIQUE INDEX role_grants_unique_idx
    ON role_grants (user_id, role_id, COALESCE(org_id, ''));

CREATE INDEX role_grants_user_idx ON role_grants(user_id);
CREATE INDEX role_grants_org_idx ON role_grants(org_id);

-- 预置 6 个内置角色。这里只写 id/name/description——
-- 权限集由 control 启动时从 Go 的权限注册表同步写入，
-- 避免权限矩阵在 Go 与 SQL 两处各存一份、迟早漂移。
INSERT INTO roles (id, name, description, is_builtin) VALUES
    ('auditor',          '审计员',        '只读：查看审计日志与组织结构', true),
    ('developer',        '开发者',        '普通成员基线：查看自己所属组织的结构', true),
    ('finops',           '财务 / FinOps', '查看全公司成本与组织结构，用于成本归属与分摊', true),
    ('org_admin',        '组织管理员',    '管理被授予节点及其子树：组织结构、成员归属、角色授予', true),
    ('platform_admin',   '平台管理员',    '全部权限', true),
    ('security_officer', '安全合规官',    '查看审计日志与组织结构（护栏策略权限在 P4 补齐）', true);

-- 把 P1.2a 的 is_platform_admin 布尔位转成全局的 platform_admin 授予。
-- 若此处一条也没生成，系统即为零管理员，CheckBootstrapConfig 会拒绝启动——
-- 是响亮的失败，不是静默降权。
INSERT INTO role_grants (id, user_id, role_id, org_id)
SELECT gen_random_uuid()::text, id, 'platform_admin', NULL
FROM users
WHERE is_platform_admin = true;

-- +goose Down
DROP TABLE role_grants;
DROP TABLE role_permissions;
DROP TABLE roles;
