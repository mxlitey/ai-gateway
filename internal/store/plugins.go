package store

import (
	"database/sql"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

const pluginCols = `id, name, version, dir, port, enabled, autostart, last_status, last_error, last_seen_at, installed_at`

func scanPlugin(sc scanner) (*model.Plugin, error) {
	p := &model.Plugin{}
	var enabled, autostart int
	if err := sc.Scan(
		&p.ID, &p.Name, &p.Version, &p.Dir, &p.Port,
		&enabled, &autostart, &p.LastStatus, &p.LastError, &p.LastSeenAt, &p.InstalledAt,
	); err != nil {
		return nil, err
	}
	p.Enabled = enabled != 0
	p.Autostart = autostart != 0
	return p, nil
}

// ListPlugins 返回全部插件记录。
func (s *Store) ListPlugins() ([]model.Plugin, error) {
	rows, err := s.db.Query(`SELECT ` + pluginCols + ` FROM plugins ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Plugin{}
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetPlugin 按 id 返回插件，不存在时返回 (nil, nil)。
func (s *Store) GetPlugin(id string) (*model.Plugin, error) {
	row := s.db.QueryRow(`SELECT `+pluginCols+` FROM plugins WHERE id = ?`, id)
	p, err := scanPlugin(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// UpsertPlugin 写入插件的基础信息；已存在时保留用户设置的 enabled / autostart。
func (s *Store) UpsertPlugin(p *model.Plugin) error {
	if p.InstalledAt == 0 {
		p.InstalledAt = time.Now().UnixMilli()
	}
	_, err := s.db.Exec(
		`INSERT INTO plugins (id, name, version, dir, port, enabled, autostart, last_status, last_error, last_seen_at, installed_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   name = excluded.name,
		   version = excluded.version,
		   dir = excluded.dir,
		   port = excluded.port`,
		p.ID, p.Name, p.Version, p.Dir, p.Port,
		boolToInt(p.Enabled), boolToInt(p.Autostart),
		p.LastStatus, p.LastError, p.LastSeenAt, p.InstalledAt,
	)
	return err
}

// SavePluginStatus 保存运行状态与错误信息。
func (s *Store) SavePluginStatus(id, status, lastErr string) error {
	_, err := s.db.Exec(
		`UPDATE plugins SET last_status = ?, last_error = ? WHERE id = ?`,
		status, lastErr, id,
	)
	return err
}

// SavePluginFlags 保存启用与自启开关。
func (s *Store) SavePluginFlags(id string, enabled, autostart bool) error {
	_, err := s.db.Exec(
		`UPDATE plugins SET enabled = ?, autostart = ? WHERE id = ?`,
		boolToInt(enabled), boolToInt(autostart), id,
	)
	return err
}

// SavePluginPort 保存解析后的端口。
func (s *Store) SavePluginPort(id string, port int) error {
	_, err := s.db.Exec(`UPDATE plugins SET port = ? WHERE id = ?`, port, id)
	return err
}

// DeletePlugin 删除插件记录。
func (s *Store) DeletePlugin(id string) error {
	_, err := s.db.Exec(`DELETE FROM plugins WHERE id = ?`, id)
	return err
}