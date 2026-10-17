package store

// KeyCooldown 一条上游密钥的冷却状态。
type KeyCooldown struct {
	ChannelID     string `json:"channel_id"`
	KeyHint       string `json:"key_hint"`
	FailStreak    int    `json:"fail_streak"`
	CooldownUntil int64  `json:"cooldown_until"`
	UpdatedAt     int64  `json:"updated_at"`
}

// UpsertKeyCooldown 写入（或覆盖）一个密钥的冷却状态。
func (s *Store) UpsertKeyCooldown(c KeyCooldown) error {
	_, err := s.db.Exec(
		`INSERT INTO key_cooldowns(channel_id, key_hint, fail_streak, cooldown_until, updated_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(channel_id, key_hint) DO UPDATE SET
		     fail_streak = excluded.fail_streak,
		     cooldown_until = excluded.cooldown_until,
		     updated_at = excluded.updated_at`,
		c.ChannelID, c.KeyHint, c.FailStreak, c.CooldownUntil, c.UpdatedAt,
	)
	return err
}

// ClearKeyCooldown 清除某个密钥的冷却（成功或手动重置时调用）。
func (s *Store) ClearKeyCooldown(channelID, keyHint string) error {
	_, err := s.db.Exec(
		`DELETE FROM key_cooldowns WHERE channel_id = ? AND key_hint = ?`,
		channelID, keyHint,
	)
	return err
}

// LoadKeyCooldowns 返回全部冷却记录（含已过期但尚未清理的），供内存预热与面板展示。
func (s *Store) LoadKeyCooldowns() ([]KeyCooldown, error) {
	rows, err := s.db.Query(
		`SELECT channel_id, key_hint, fail_streak, cooldown_until, updated_at FROM key_cooldowns`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []KeyCooldown{}
	for rows.Next() {
		var c KeyCooldown
		if err := rows.Scan(&c.ChannelID, &c.KeyHint, &c.FailStreak, &c.CooldownUntil, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PurgeExpiredKeyCooldowns 清理冷却已结束且无失败计数的记录，避免表无限增长。
func (s *Store) PurgeExpiredKeyCooldowns(now int64) error {
	_, err := s.db.Exec(
		`DELETE FROM key_cooldowns WHERE cooldown_until > 0 AND cooldown_until <= ? AND fail_streak = 0`,
		now,
	)
	return err
}

// KeyCooldownView 面板展示用的冷却条目：附带渠道名等冗余信息。
type KeyCooldownView struct {
	KeyCooldown
	ChannelName string `json:"channel_name"`
	// RemainingSeconds 距冷却结束的剩余秒数（<=0 表示已过期）。
	RemainingSeconds int64 `json:"remaining_seconds"`
}

// ListActiveKeyCooldowns 返回当前仍在冷却中的条目（含渠道名），按剩余时间升序。
func (s *Store) ListActiveKeyCooldowns(now int64) ([]KeyCooldownView, error) {
	rows, err := s.db.Query(
		`SELECT k.channel_id, k.key_hint, k.fail_streak, k.cooldown_until, k.updated_at,
		        COALESCE(c.name, '')
		 FROM key_cooldowns k
		 LEFT JOIN channels c ON c.id = k.channel_id
		 WHERE k.cooldown_until > ?
		 ORDER BY k.cooldown_until ASC`,
		now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []KeyCooldownView{}
	for rows.Next() {
		var v KeyCooldownView
		if err := rows.Scan(
			&v.ChannelID, &v.KeyHint, &v.FailStreak, &v.CooldownUntil, &v.UpdatedAt, &v.ChannelName,
		); err != nil {
			return nil, err
		}
		v.RemainingSeconds = (v.CooldownUntil - now) / 1000
		if v.RemainingSeconds < 0 {
			v.RemainingSeconds = 0
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
