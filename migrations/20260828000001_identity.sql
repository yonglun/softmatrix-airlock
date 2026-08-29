-- +goose Up
CREATE TABLE users (
    id                TEXT PRIMARY KEY,
    -- external_id 是 OIDC 的 sub，跨 IdP 稳定且唯一
    external_id       TEXT NOT NULL UNIQUE,
    email             TEXT NOT NULL,
    display_name      TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active', 'disabled')),
    -- P1.2b 引入 role_grants 后，此布尔位迁移为正式角色
    is_platform_admin BOOLEAN NOT NULL DEFAULT false,
    -- 主组织决定该用户名下 Key 的默认计费归属
    primary_org_id    TEXT REFERENCES organizations(id) ON DELETE RESTRICT,
    last_login_at     TIMESTAMPTZ,
    -- 最近一次与 IdP 对账的时间
    reconciled_at     TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX users_primary_org_idx ON users(primary_org_id);
CREATE INDEX users_status_idx ON users(status);

CREATE TABLE sessions (
    -- id 是 session token 的 sha256，不存原始 token：
    -- 数据库泄露不等于会话被劫持，与 api_keys.key_hash 同一原则
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at   TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ip           TEXT NOT NULL DEFAULT '',
    user_agent   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX sessions_user_idx ON sessions(user_id);
CREATE INDEX sessions_expiry_idx ON sessions(expires_at);

-- login_states 承载 OAuth2 授权码流程中 /login 与 /callback 之间的一次性状态。
-- 放服务端而不是签名 cookie，是为了让 state 与 PKCE verifier 不可被浏览器侧篡改。
CREATE TABLE login_states (
    id            TEXT PRIMARY KEY,
    state         TEXT NOT NULL,
    pkce_verifier TEXT NOT NULL,
    redirect_to   TEXT NOT NULL DEFAULT '/',
    expires_at    TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX login_states_expiry_idx ON login_states(expires_at);

ALTER TABLE organizations
    ADD COLUMN external_source TEXT,
    ADD COLUMN external_id     TEXT,
    ADD COLUMN updated_at      TIMESTAMPTZ NOT NULL DEFAULT now();

-- 同一 IdP 来源下 external_id 唯一，保证重复导入幂等；
-- 手工创建的节点 external_source 为 NULL，不受此约束
CREATE UNIQUE INDEX organizations_external_idx
    ON organizations(external_source, external_id)
    WHERE external_source IS NOT NULL;

-- +goose Down
DROP INDEX organizations_external_idx;
ALTER TABLE organizations
    DROP COLUMN external_source,
    DROP COLUMN external_id,
    DROP COLUMN updated_at;
DROP TABLE login_states;
DROP TABLE sessions;
DROP TABLE users;
