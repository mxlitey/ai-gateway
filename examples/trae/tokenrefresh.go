package main

import (
	"context"
	"log"
	"strings"
	"time"
)

// 后台任务（设计稿 §8）：token 到期前预刷新。
// 自动签到**不在这里**：已改由网关宿主统一调度（见 docs/plugin-spec.md §6.1），
// 插件只需实现 POST /console/accounts/{id}/checkin，不再自行定时签到。

const (
	bgInterval  = 30 * time.Minute // 后台复查间隔
	refreshSkew = 24 * time.Hour   // 到期前多久预刷新 token
)

// startBackground 启动后台任务；ctx 取消即退出。
func (p *Plugin) startBackground(ctx context.Context) {
	go func() {
		// 启动时先补一次临期 token 的预刷新。
		p.runTokenPreRefresh()

		ticker := time.NewTicker(bgInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.runTokenPreRefresh()
			}
		}
	}()
}

// runTokenPreRefresh 对 24 小时内即将过期的账号做 token 预刷新并原子落盘。
func (p *Plugin) runTokenPreRefresh() {
	now := time.Now().Unix()
	for _, a := range p.store.list() {
		if strings.TrimSpace(a.RefreshToken) == "" {
			continue
		}
		if a.ExpiresAt <= 0 || a.ExpiresAt-now > int64(refreshSkew/time.Second) {
			continue
		}
		acc := a
		if err := p.refreshToken(&acc); err != nil {
			log.Printf("[token 预刷新] %s 失败：%v", a.name(), err)
			continue
		}
		if err := p.store.saveAuth(acc.toAuth()); err != nil {
			log.Printf("[token 预刷新] %s 落盘失败：%v", a.name(), err)
			continue
		}
		log.Printf("[token 预刷新] %s 完成", a.name())
	}
}