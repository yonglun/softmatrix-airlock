-- +goose Up
-- P1.1 端到端验收往 api_keys 写入过 user_id 指向不存在用户的测试数据
-- （典型为 'user1'）。给 user_id 加外键之前必须先清理，否则约束创建失败。
-- 这些行是验收残留，不是业务数据——执行前已人工核对过表内容。
DELETE FROM api_keys
WHERE user_id NOT IN (SELECT id FROM users);

ALTER TABLE api_keys
    ADD CONSTRAINT api_keys_user_fk
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE api_keys DROP CONSTRAINT api_keys_user_fk;
