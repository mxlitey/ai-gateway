package main

import (
	"context"
	"log"
	"time"
)

// autoCheckinInterval 自动签到复查间隔。
const autoCheckinInterval = time.Hour

// startAutoCheckin 后台自动签到：启动时先补签当天未签到的账号，之后每小时复查一次。
func (p *Plugin) startAutoCheckin(ctx context.Context) {
	go func() {
		p.checkinPending()
		ticker := time.NewTicker(autoCheckinInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.checkinPending()
			}
		}
	}()
}

// checkinPending 遍历账号，把「上游确认今天还没签到」的补签掉。
//
// 以远端状态为准：本地 CheckedInToday 可能停留在昨天，跨天后不可信，
// 因此先查一次签到状态，只对确认未签到的账号执行签到。
func (p *Plugin) checkinPending() {
	accounts := p.store.list()
	for i := range accounts {
		acc := accounts[i]
		if !p.realmCfg(acc.Site).HasCheckin {
			continue
		}
		if err := p.ensureToken(&acc); err != nil {
			log.Printf("[自动签到] %s 跳过：%v", acc.Name, err)
			continue
		}

		st, err := p.fetchCheckinStatus(&acc)
		if err != nil {
			log.Printf("[自动签到] %s 状态查询失败：%v", acc.Name, err)
			continue
		}
		p.store.update(acc.ID, func(a *Account) {
			a.CheckedInToday = st.CheckedInToday
			if st.StreakDays > 0 {
				a.StreakDays = st.StreakDays
			}
		})
		if st.CheckedInToday {
			continue
		}

		res, msg, err := p.doCheckin(&acc)
		if err != nil {
			p.store.update(acc.ID, func(a *Account) { a.LastError = err.Error() })
			log.Printf("[自动签到] %s 失败：%v", acc.Name, err)
			continue
		}
		p.store.update(acc.ID, func(a *Account) {
			a.CheckedInToday = res.CheckedInToday
			a.LastError = ""
			if res.StreakDays > 0 {
				a.StreakDays = res.StreakDays
			}
			if res.TodayCredit > 0 {
				a.TodayCredit = res.TodayCredit
			}
		})
		log.Printf("[自动签到] %s 完成：%s", acc.Name, msg)
	}
}