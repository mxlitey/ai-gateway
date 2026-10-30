package store

import "strconv"

// boolStr 把布尔值转为落库形式 "1"/"0"。
func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// 运行期设置的持久化键。与部署级参数（监听地址、端口、数据目录）区分：
// 后者由 CLI / 环境变量决定且面板只读，这里都是可在面板中调整的运行期偏好。
const (
	SettingRuntimeLogLevel                  = "runtime.log_level"
	SettingRuntimePriceEndpoint             = "runtime.price_endpoint"
	SettingRuntimePriceRefreshMinutes       = "runtime.price_refresh_minutes"
	SettingRuntimePluginProbeIntervalSecs   = "runtime.plugin_probe_interval_seconds"
	SettingRuntimePluginProbeTimeoutSecs    = "runtime.plugin_probe_timeout_seconds"
	SettingRuntimePluginUnhealthyThreshold  = "runtime.plugin_unhealthy_threshold"
	SettingRuntimePluginMaxFailures         = "runtime.plugin_max_failures"
	SettingRuntimePluginAutoCheckinMinutes  = "runtime.plugin_auto_checkin_minutes"

	// 上游密钥失败冷却
	SettingRuntimeKeyCooldownEnabled  = "runtime.key_cooldown_enabled"
	SettingRuntimeKeyCooldownBaseSec  = "runtime.key_cooldown_base_seconds"
	SettingRuntimeKeyCooldownMaxSec   = "runtime.key_cooldown_max_seconds"

	// 管理 MCP（/admin/mcp）：把网关自身的管理能力暴露为 MCP 工具。
	// 与 RuntimeSettings 分开存：这几项含凭据摘要，不参与整体覆盖式保存。
	SettingAdminMCPEnabled  = "admin_mcp.enabled"
	SettingAdminMCPWritable = "admin_mcp.writable"
	// SettingAdminMCPTokenHash 管理令牌的 SHA-256 摘要（十六进制），明文不落库。
	SettingAdminMCPTokenHash = "admin_mcp.token_hash"
)

// RuntimeSettings 运行期设置：面板可改，多数立即生效（无需重启）。
//
// 字符串项留空表示「未显式设置」，由调用方回退到启动参数 / 环境变量 / 内置默认；
// 数值项缺省时直接取 DefaultRuntimeSettings 中的默认值。
type RuntimeSettings struct {
	// LogLevel 日志级别 debug|info|warn|error；留空表示沿用启动参数 / 环境变量。
	LogLevel string `json:"log_level"`
	// PriceEndpoint 模型价格清单地址；留空表示沿用环境变量 AIGW_PRICE_ENDPOINT 或默认地址。
	PriceEndpoint string `json:"price_endpoint"`
	// PriceRefreshMinutes 价格快照复查间隔（分钟）。
	PriceRefreshMinutes int `json:"price_refresh_minutes"`

	// PluginProbeIntervalSeconds 插件探活间隔（秒）。
	PluginProbeIntervalSeconds int `json:"plugin_probe_interval_seconds"`
	// PluginProbeTimeoutSeconds 单次探活超时（秒）。
	PluginProbeTimeoutSeconds int `json:"plugin_probe_timeout_seconds"`
	// PluginUnhealthyThreshold 连续探活失败多少次判定不健康并触发重启。
	PluginUnhealthyThreshold int `json:"plugin_unhealthy_threshold"`
	// PluginMaxFailures 连续失败多少次后放弃自动重启。
	PluginMaxFailures int `json:"plugin_max_failures"`
	// PluginAutoCheckinMinutes 自动签到复查间隔（分钟）。
	PluginAutoCheckinMinutes int `json:"plugin_auto_checkin_minutes"`

	// KeyCooldownEnabled 是否启用上游密钥失败冷却。
	KeyCooldownEnabled bool `json:"key_cooldown_enabled"`
	// KeyCooldownBaseSeconds 首次失败的冷却时长（秒）。
	KeyCooldownBaseSeconds int `json:"key_cooldown_base_seconds"`
	// KeyCooldownMaxSeconds 单次冷却时长上限（秒）。
	KeyCooldownMaxSeconds int `json:"key_cooldown_max_seconds"`
}

// DefaultRuntimeSettings 返回运行期设置的默认值。
func DefaultRuntimeSettings() RuntimeSettings {
	return RuntimeSettings{
		LogLevel:                   "",
		PriceEndpoint:              "",
		PriceRefreshMinutes:        60,
		PluginProbeIntervalSeconds: 2,
		PluginProbeTimeoutSeconds:  2,
		PluginUnhealthyThreshold:   3,
		PluginMaxFailures:          5,
		PluginAutoCheckinMinutes:   60,
		KeyCooldownEnabled:         true,
		KeyCooldownBaseSeconds:     60,
		KeyCooldownMaxSeconds:      1800,
	}
}

// LoadRuntimeSettings 读取运行期设置：未落库的项回退为默认值。
func (s *Store) LoadRuntimeSettings() (RuntimeSettings, error) {
	out := DefaultRuntimeSettings()

	if v, ok, err := s.GetSetting(SettingRuntimeLogLevel); err != nil {
		return out, err
	} else if ok {
		out.LogLevel = v
	}
	if v, ok, err := s.GetSetting(SettingRuntimePriceEndpoint); err != nil {
		return out, err
	} else if ok {
		out.PriceEndpoint = v
	}
	// 布尔项：落库值为 "1"/"0"
	if v, ok, err := s.GetSetting(SettingRuntimeKeyCooldownEnabled); err != nil {
		return out, err
	} else if ok {
		out.KeyCooldownEnabled = v == "1"
	}

	ints := []struct {
		key   string
		apply func(int)
	}{
		{SettingRuntimePriceRefreshMinutes, func(n int) { out.PriceRefreshMinutes = n }},
		{SettingRuntimePluginProbeIntervalSecs, func(n int) { out.PluginProbeIntervalSeconds = n }},
		{SettingRuntimePluginProbeTimeoutSecs, func(n int) { out.PluginProbeTimeoutSeconds = n }},
		{SettingRuntimePluginUnhealthyThreshold, func(n int) { out.PluginUnhealthyThreshold = n }},
		{SettingRuntimePluginMaxFailures, func(n int) { out.PluginMaxFailures = n }},
		{SettingRuntimePluginAutoCheckinMinutes, func(n int) { out.PluginAutoCheckinMinutes = n }},
		{SettingRuntimeKeyCooldownBaseSec, func(n int) { out.KeyCooldownBaseSeconds = n }},
		{SettingRuntimeKeyCooldownMaxSec, func(n int) { out.KeyCooldownMaxSeconds = n }},
	}
	for _, item := range ints {
		v, ok, err := s.GetSetting(item.key)
		if err != nil {
			return out, err
		}
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			item.apply(n)
		}
	}
	return out, nil
}

// SaveRuntimeSettings 落库运行期设置（整体覆盖）。
func (s *Store) SaveRuntimeSettings(rs RuntimeSettings) error {
	pairs := []struct {
		key   string
		value string
	}{
		{SettingRuntimeLogLevel, rs.LogLevel},
		{SettingRuntimePriceEndpoint, rs.PriceEndpoint},
		{SettingRuntimePriceRefreshMinutes, strconv.Itoa(rs.PriceRefreshMinutes)},
		{SettingRuntimePluginProbeIntervalSecs, strconv.Itoa(rs.PluginProbeIntervalSeconds)},
		{SettingRuntimePluginProbeTimeoutSecs, strconv.Itoa(rs.PluginProbeTimeoutSeconds)},
		{SettingRuntimePluginUnhealthyThreshold, strconv.Itoa(rs.PluginUnhealthyThreshold)},
		{SettingRuntimePluginMaxFailures, strconv.Itoa(rs.PluginMaxFailures)},
		{SettingRuntimePluginAutoCheckinMinutes, strconv.Itoa(rs.PluginAutoCheckinMinutes)},
		{SettingRuntimeKeyCooldownEnabled, boolStr(rs.KeyCooldownEnabled)},
		{SettingRuntimeKeyCooldownBaseSec, strconv.Itoa(rs.KeyCooldownBaseSeconds)},
		{SettingRuntimeKeyCooldownMaxSec, strconv.Itoa(rs.KeyCooldownMaxSeconds)},
	}
	for _, p := range pairs {
		if err := s.SetSetting(p.key, p.value); err != nil {
			return err
		}
	}
	return nil
}
