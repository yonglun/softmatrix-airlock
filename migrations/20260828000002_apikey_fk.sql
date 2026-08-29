-- +goose Up
-- 给 api_keys.user_id 加外键指向 users(id)。
--
-- 障碍：P1.1 阶段 api_keys.user_id 是裸 TEXT 无外键，端到端验收往里写过
-- user_id 指向不存在用户的测试数据（典型为 'user1'）。不清理就建不起约束。
--
-- 危险：紧邻的上一个迁移（20260828000001）刚创建了**空的** users 表。
-- 因此「删掉 user_id 不在 users 里的行」在一个已有真实密钥的库上，
-- 会匹配到**每一行**——静默删光客户的全部 API 密钥。
--
-- 所以这里不无条件删除，而是先数一遍：超过阈值就直接报错中止，
-- 逼运维停下来人工判断，而不是让升级过程悄悄毁掉生产数据。
-- 阈值取 5：开发库里的验收残留是个位数，真实环境的密钥表远不止这个量级。

-- +goose StatementBegin
DO $$
DECLARE
    orphan_count integer;
BEGIN
    SELECT count(*) INTO orphan_count
    FROM api_keys
    WHERE user_id NOT IN (SELECT id FROM users);

    IF orphan_count > 5 THEN
        RAISE EXCEPTION
            '拒绝自动删除 % 行 api_keys。这些行的 user_id 在 users 表中不存在，'
            '但数量过多，不像是开发环境的验收残留——很可能是尚未迁移到 users 表的'
            '真实业务数据。请先人工确认这些密钥的归属并补齐 users 记录，'
            '或在确认可丢弃后手动删除，然后重新执行迁移。',
            orphan_count;
    END IF;

    IF orphan_count > 0 THEN
        RAISE NOTICE '清理 % 行 user_id 悬空的 api_keys（判定为开发环境验收残留）', orphan_count;
        DELETE FROM api_keys WHERE user_id NOT IN (SELECT id FROM users);
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE api_keys
    ADD CONSTRAINT api_keys_user_fk
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE api_keys DROP CONSTRAINT api_keys_user_fk;
