package main

import (
	"context"
	"log"
	"strings"
	"time"
)

// 后台任务（设计稿 §8）：每日定时签到 + token 到期前预刷新。
// **不做额度定时轮询** —— 额度快照只在「签到」与「额度刷新」两个接口被调用时更新。

const (
	bgInterval  = 30 * time.Minute // 后台复查间隔
	refreshSkew = 24 * time.Hour   // 到期前多久预刷新 token
)

// startBackground 启动后台任务；ctx 取消即退出。
func (p *Plugin) startBackground(ctx context.Context) {
	go func() {
		// 启动时先补一次（补当天未签到 + 预刷新临期 token）。
		p.runDailyCheckin()
		p.runTokenPreRefresh()

		lastDay := time.Now().Format("2006-01-02")
		ticker := time.NewTicker(bgInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if today := time.Now().Format("2006-01-02"); today != lastDay {
					lastDay = today
					p.runDailyCheckin()
				}
				p.runTokenPreRefresh()
			}
		}
	}()
}

// runDailyCheckin 遍历可用账号执行签到（幂等）。
func (p *Plugin) runDailyCheckin() {
	for _, a := range p.store.list() {
		if !a.usable() {
			continue
		}
		if _, _, err := p.performCheckin(a.UID); err != nil {
			log.Printf("[自动签到] %s 失败：%v", a.name(), err)
			continue
		}
		log.Printf("[自动签到] %s 完成", a.name())
	}
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