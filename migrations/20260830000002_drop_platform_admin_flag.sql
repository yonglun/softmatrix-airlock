-- +goose Up
-- 留着这一列会有两个「谁是管理员」的真相来源，迟早漂移，
-- 而这正是最容易长出安全漏洞的地方。数据已在上一个迁移里
-- 转成了全局 platform_admin 授予。
ALTER TABLE users DROP COLUMN is_platform_admin;

-- +goose Down
ALTER TABLE users ADD COLUMN is_platform_admin BOOLEAN NOT NULL DEFAULT false;

UPDATE users SET is_platform_admin = true
WHERE id IN (
    SELECT user_id FROM role_grants
    WHERE role_id = 'platform_admin' AND org_id IS NULL
);
