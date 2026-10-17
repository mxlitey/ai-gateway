-- 上游密钥失败冷却：请求上游失败时对该密钥做冷却，冷却期内不参与路由。
--
-- 以 (渠道, 密钥脱敏值) 为唯一键；key_hint 由 model.KeyHint 生成，避免在库里再存一份完整密钥。
-- cooldown_until 存绝对毫秒时间戳，重启后过期的自动失效、未过期的继续生效。
-- fail_streak 表示连续失败次数（成功即清零），决定冷却时长按倍数递增。
CREATE TABLE IF NOT EXISTS key_cooldowns (
    channel_id     TEXT    NOT NULL,
    key_hint       TEXT    NOT NULL,
    fail_streak    INTEGER NOT NULL DEFAULT 0,
    cooldown_until INTEGER NOT NULL DEFAULT 0,
    updated_at     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (channel_id, key_hint)
);
CREATE INDEX IF NOT EXISTS idx_key_cooldowns_until ON key_cooldowns(cooldown_until);
