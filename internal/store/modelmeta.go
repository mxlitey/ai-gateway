package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

// ---------- 生图模型元数据 ----------
//
// 只存「人工声明的约束」，不存耗时、错误码这类观测数据（那些直接查 request_logs）。
// 主键是公开模型名，与渠道无关：同一模型挂多个渠道只声明一次。

const modelMetaCols = `model, spec, updated_at`

func scanModelMeta(sc scanner) (*model.ModelMeta, error) {
	var name, spec string
	m := &model.ModelMeta{}
	if err := sc.Scan(&name, &spec, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.Model = name
	if spec != "" && spec != "{}" {
		if err := json.Unmarshal([]byte(spec), m); err != nil {
			return nil, err
		}
		// spec 里不含主键与时间，回填后再返回，避免被 JSON 里的空值覆盖
		m.Model = name
	}
	return m, nil
}

// ListModelMetas 返回全部模型的元数据声明（按模型名升序）。
func (s *Store) ListModelMetas() ([]model.ModelMeta, error) {
	rows, err := s.db.Query(`SELECT ` + modelMetaCols + ` FROM model_meta ORDER BY model ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.ModelMeta{}
	for rows.Next() {
		m, err := scanModelMeta(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// GetModelMeta 按公开模型名查询，不存在返回 (nil, nil)。
func (s *Store) GetModelMeta(name string) (*model.ModelMeta, error) {
	m, err := scanModelMeta(s.db.QueryRow(
		`SELECT `+modelMetaCols+` FROM model_meta WHERE model = ?`, name))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// UpsertModelMeta 写入或整体覆盖一条模型元数据。
func (s *Store) UpsertModelMeta(m *model.ModelMeta) error {
	if m.UpdatedAt == 0 {
		m.UpdatedAt = time.Now().UnixMilli()
	}
	// 主键、计算字段不入 spec：前者是列，后者由接口现算
	spec := *m
	spec.Model = ""
	spec.HasChannel = false
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO model_meta(`+modelMetaCols+`) VALUES(?,?,?)
		 ON CONFLICT(model) DO UPDATE SET
		   spec = excluded.spec,
		   updated_at = excluded.updated_at`,
		m.Model, string(raw), m.UpdatedAt,
	)
	return err
}

// DeleteModelMeta 删除一条模型元数据。
func (s *Store) DeleteModelMeta(name string) error {
	_, err := s.db.Exec(`DELETE FROM model_meta WHERE model = ?`, name)
	return err
}
