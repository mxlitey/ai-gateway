package storage

import (
	"errors"
	"strings"
	"time"
)

// Config 面板「设置 → 对象存储」保存的配置；对象存储只能通过面板配置，不支持启动参数。
type Config struct {
	// Driver none（不启用）| s3；空值等同 none。
	Driver         string
	Endpoint       string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	Prefix         string
	PublicBase     string
	ForcePathStyle string // on / off；空值等同 off
	TTLSeconds     int
}

// Validate 校验面板提交的配置。
func (c Config) Validate() error {
	switch v := strings.ToLower(strings.TrimSpace(c.Driver)); v {
	case "", "none", "s3":
	default:
		return errors.New("对象存储驱动需为 none 或 s3")
	}
	if v := strings.ToLower(strings.TrimSpace(c.ForcePathStyle)); v != "" && v != "on" && v != "off" {
		return errors.New("path-style 需为 on 或 off")
	}
	if c.TTLSeconds < 0 {
		return errors.New("预签名有效期不能为负数")
	}
	if c.TTLSeconds > 604800 {
		return errors.New("预签名有效期不能超过 604800 秒（7 天）")
	}
	return nil
}

// enabled 报告配置是否指向可用的对象存储。
func (c Config) enabled() bool {
	return strings.ToLower(strings.TrimSpace(c.Driver)) == "s3"
}

// Options 把面板配置转成句柄参数。
// 未启用或必填项不全时返回零值 Options，New 会因此返回 nil，接口回 501。
func (c Config) Options() Options {
	if !c.enabled() {
		return Options{}
	}
	opts := Options{
		Endpoint:       strings.TrimRight(strings.TrimSpace(c.Endpoint), "/"),
		Region:         strings.TrimSpace(c.Region),
		Bucket:         strings.TrimSpace(c.Bucket),
		AccessKey:      strings.TrimSpace(c.AccessKey),
		SecretKey:      strings.TrimSpace(c.SecretKey),
		Prefix:         strings.Trim(strings.TrimSpace(c.Prefix), "/"),
		PublicBase:     strings.TrimRight(strings.TrimSpace(c.PublicBase), "/"),
		ForcePathStyle: strings.ToLower(strings.TrimSpace(c.ForcePathStyle)) == "on",
	}
	if c.TTLSeconds > 0 {
		opts.PresignTTL = time.Duration(c.TTLSeconds) * time.Second
	}
	if opts.PresignTTL <= 0 {
		opts.PresignTTL = 30 * time.Minute
	}
	return opts
}
