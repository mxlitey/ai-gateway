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
	SettingRuntimeLogLevel                 = "runtime.log_level"
	SettingRuntimeCurrencySymbol           = "runtime.currency_symbol"
	SettingRuntimePluginProbeIntervalSecs  = "runtime.plugin_probe_interval_seconds"
	SettingRuntimePluginProbeTimeoutSecs   = "runtime.plugin_probe_timeout_seconds"
	SettingRuntimePluginUnhealthyThreshold = "runtime.plugin_unhealthy_threshold"
	SettingRuntimePluginMaxFailures        = "runtime.plugin_max_failures"
	SettingRuntimePluginAutoCheckinMinutes = "runtime.plugin_auto_checkin_minutes"

	// 上游密钥失败冷却
	SettingRuntimeKeyCooldownEnabled = "runtime.key_cooldown_enabled"
	SettingRuntimeKeyCooldownBaseSec = "runtime.key_cooldown_base_seconds"
	SettingRuntimeKeyCooldownMaxSec  = "runtime.key_cooldown_max_seconds"

	// 管理 MCP（/admin/mcp）：把网关自身的管理能力暴露为 MCP 工具。
	// 与 RuntimeSettings 分开存：这几项含凭据摘要，不参与整体覆盖式保存。
	SettingAdminMCPEnabled = "admin_mcp.enabled"
	// SettingAdminMCPReadOnlyTokenHash / SettingAdminMCPTokenHash 两枚令牌的 SHA-256 摘要（十六进制），明文不落库。
	// 只读令牌只能调用查询类工具，读写令牌可调用全部工具。
	SettingAdminMCPReadOnlyTokenHash = "admin_mcp.readonly_token_hash"
	SettingAdminMCPTokenHash         = "admin_mcp.token_hash"
	// SettingAdminMCPWritable 旧版「是否允许写操作」开关，现已由令牌区分权限，仅用于识别旧版单令牌的档位。
	SettingAdminMCPWritable = "admin_mcp.writable"
)

// 对象存储（S3 兼容）设置。与 RuntimeSettings 分开存：
// 含凭据，需要可回显掩码，且保存后要立即重建预签名句柄。
const (
	SettingStorageDriver         = "storage.driver"
	SettingStorageEndpoint       = "storage.s3_endpoint"
	SettingStorageRegion         = "storage.s3_region"
	SettingStorageBucket         = "storage.s3_bucket"
	SettingStorageAccessKey      = "storage.s3_access_key"
	SettingStorageSecretKey      = "storage.s3_secret_key"
	SettingStoragePrefix         = "storage.s3_prefix"
	SettingStoragePresignTTL     = "storage.s3_presign_ttl_seconds"
	SettingStoragePublicBase     = "storage.s3_public_base"
	SettingStorageForcePathStyle = "storage.s3_force_path_style"
)

// StorageSettings 对象存储设置：只能通过面板配置，未落库项为零值。
type StorageSettings struct {
	// Driver 驱动：none 不启用；s3 使用 S3 兼容协议。
	Driver string `json:"driver"`
	// Endpoint 服务端点，如 https://s3.us-east-1.amazonaws.com 或自建 MinIO 地址。
	Endpoint string `json:"endpoint"`
	// Region 签名区域。
	Region string `json:"region"`
	// Bucket 目标桶名。
	Bucket string `json:"bucket"`
	// AccessKey / SecretKey 预签名凭证；SecretKey 读取时脱敏。
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	// Prefix 对象键前缀，可为空。
	Prefix string `json:"prefix"`
	// PresignTTLSeconds 预签名有效期（秒）；0 表示用默认值 1800。
	PresignTTLSeconds int `json:"presign_ttl_seconds"`
	// PublicBase 公开访问前缀（CDN 或公开桶），可为空。
	PublicBase string `json:"public_base"`
	// ForcePathStyle path-style 开关：on 使用；off（或空）不使用。
	ForcePathStyle string `json:"force_path_style"`
}

// LoadStorageSettings 读取对象存储设置：未落库的项保持零值。
func (s *Store) LoadStorageSettings() (StorageSettings, error) {
	var out StorageSettings
	strs := []struct {
		key string
		dst *string
	}{
		{SettingStorageDriver, &out.Driver},
		{SettingStorageEndpoint, &out.Endpoint},
		{SettingStorageRegion, &out.Region},
		{SettingStorageBucket, &out.Bucket},
		{SettingStorageAccessKey, &out.AccessKey},
		{SettingStorageSecretKey, &out.SecretKey},
		{SettingStoragePrefix, &out.Prefix},
		{SettingStoragePublicBase, &out.PublicBase},
		{SettingStorageForcePathStyle, &out.ForcePathStyle},
	}
	for _, item := range strs {
		v, ok, err := s.GetSetting(item.key)
		if err != nil {
			return out, err
		}
		if ok {
			*item.dst = v
		}
	}
	if v, ok, err := s.GetSetting(SettingStoragePresignTTL); err != nil {
		return out, err
	} else if ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			out.PresignTTLSeconds = n
		}
	}
	return out, nil
}

// SaveStorageSettings 落库对象存储设置（整体覆盖）。
func (s *Store) SaveStorageSettings(ss StorageSettings) error {
	pairs := []struct {
		key   string
		value string
	}{
		{SettingStorageDriver, ss.Driver},
		{SettingStorageEndpoint, ss.Endpoint},
		{SettingStorageRegion, ss.Region},
		{SettingStorageBucket, ss.Bucket},
		{SettingStorageAccessKey, ss.AccessKey},
		{SettingStorageSecretKey, ss.SecretKey},
		{SettingStoragePrefix, ss.Prefix},
		{SettingStoragePresignTTL, strconv.Itoa(ss.PresignTTLSeconds)},
		{SettingStoragePublicBase, ss.PublicBase},
		{SettingStorageForcePathStyle, ss.ForcePathStyle},
	}
	for _, p := range pairs {
		if err := s.SetSetting(p.key, p.value); err != nil {
			return err
		}
	}
	return nil
}

// RuntimeSettings 运行期设置：面板可改，多数立即生效（无需重启）。
//
// 字符串项留空表示「未显式设置」，由调用方回退到启动参数 / 环境变量 / 内置默认；
// 数值项缺省时直接取 DefaultRuntimeSettings 中的默认值。
type RuntimeSettings struct {
	// LogLevel 日志级别 debug|info|warn|error；留空表示沿用启动参数 / 环境变量。
	LogLevel string `json:"log_level"`
	// CurrencySymbol 价格与费用展示用的货币符号（如 ¥ / $ / €）；留空只显示数字。
	// 费用本身是纯数字，填什么symbol就代表什么货币，不做任何换算。
	CurrencySymbol string `json:"currency_symbol"`

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
		CurrencySymbol:             "¥",
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
	if v, ok, err := s.GetSetting(SettingRuntimeCurrencySymbol); err != nil {
		return out, err
	} else if ok {
		// 允许显式留空（不显示符号），因此与默认值区分：落库过就以落库值为准
		out.CurrencySymbol = v
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
		{SettingRuntimeCurrencySymbol, rs.CurrencySymbol},
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
