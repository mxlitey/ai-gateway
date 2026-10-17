//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/czg95/ai-gateway-v2/internal/config"
	"github.com/czg95/ai-gateway-v2/internal/plugin"
)

// AddPlugin 弹出原生目录选择，把选中的插件目录复制进插件目录并重新扫描。
// 网关未运行时只落盘（不触碰插件宿主，避免拉起插件进程），启动后自动注册。
func (s *GatewayService) AddPlugin() actionResult {
	s.mu.Lock()
	app, gw, cfg := s.wa, s.gw, s.cfg
	s.mu.Unlock()
	if app == nil || cfg == nil {
		return actionResult{OK: false, Message: "服务尚未就绪"}
	}

	src, err := app.Dialog.OpenFile().
		SetTitle("选择插件目录（目录内需有 plugin.json）").
		CanChooseDirectories(true).
		PromptForSingleSelection()
	if err != nil {
		return actionResult{OK: false, Message: "选择目录失败：" + err.Error()}
	}
	if src == "" {
		return actionResult{OK: true, Message: ""} // 用户取消
	}

	if _, err := plugin.LoadManifest(src); err != nil {
		return actionResult{OK: false, Message: "不是有效的插件目录：" + err.Error()}
	}
	if err := os.MkdirAll(cfg.PluginsDir, 0o755); err != nil {
		return actionResult{OK: false, Message: "创建插件目录失败：" + err.Error()}
	}
	dst := filepath.Join(cfg.PluginsDir, filepath.Base(src))
	if err := copyDir(src, dst); err != nil {
		return actionResult{OK: false, Message: "复制插件失败：" + err.Error()}
	}

	msg := "已添加插件：" + filepath.Base(src)
	if gw == nil || gw.Host() == nil {
		msg += "（启动网关后生效）"
	} else if err := gw.Host().Discover(); err != nil {
		return actionResult{OK: false, Message: "重新扫描失败：" + err.Error()}
	}

	s.appendLog(msg)
	return actionResult{OK: true, Message: msg}
}

// RemovePlugin 删除插件，连同其目录与它注册的渠道。
// 网关未运行时直接删除磁盘目录；注册记录与渠道会在下次启动网关时清理。
func (s *GatewayService) RemovePlugin(id string) actionResult {
	s.mu.Lock()
	gw, cfg := s.gw, s.cfg
	s.mu.Unlock()
	if cfg == nil {
		return actionResult{OK: false, Message: "服务尚未就绪"}
	}

	if gw != nil && gw.Host() != nil {
		name := id
		for _, p := range gw.Host().List() {
			if p.ID == id {
				name = p.Name
				break
			}
		}
		if err := gw.Host().Remove(id); err != nil {
			return actionResult{OK: false, Message: "删除失败：" + err.Error()}
		}
		msg := "已删除插件：" + name
		s.appendLog(msg)
		return actionResult{OK: true, Message: msg}
	}

	for _, p := range scanPluginRows(cfg) {
		if p.ID != id {
			continue
		}
		if err := os.RemoveAll(p.Dir); err != nil {
			return actionResult{OK: false, Message: "删除失败：" + err.Error()}
		}
		msg := "已删除插件：" + p.Name + "（相关渠道将在网关启动时清理）"
		s.appendLog(msg)
		return actionResult{OK: true, Message: msg}
	}
	return actionResult{OK: false, Message: "未找到该插件的目录"}
}

// Rescan 重新扫描插件目录。网关未运行时退化为按磁盘重新读取，保证停止状态下也能刷新列表。
func (s *GatewayService) Rescan() actionResult {
	s.mu.Lock()
	gw, cfg := s.gw, s.cfg
	s.mu.Unlock()
	if gw == nil || gw.Host() == nil {
		msg := fmt.Sprintf("网关未运行，已按磁盘重新读取 %d 个插件（启动网关后生效）", len(scanPluginRows(cfg)))
		s.appendLog(msg)
		return actionResult{OK: true, Message: msg}
	}
	if err := gw.Host().Discover(); err != nil {
		return actionResult{OK: false, Message: "扫描失败：" + err.Error()}
	}
	s.appendLog("已重新扫描插件目录")
	return actionResult{OK: true, Message: "已重新扫描插件目录"}
}

// scanPluginRows 网关未运行时直接从磁盘读取插件目录（识别口径与宿主 Discover 一致：
// 含 plugin.json 的一级子目录）。状态一律显示「已停止」，端口用清单声明的端口。
func scanPluginRows(cfg *config.Config) []pluginRow {
	rows := []pluginRow{}
	if cfg == nil {
		return rows
	}
	entries, err := os.ReadDir(cfg.PluginsDir)
	if err != nil {
		return rows
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(cfg.PluginsDir, e.Name())
		m, err := plugin.LoadManifest(dir)
		if err != nil {
			continue // 不是插件目录或清单不可读
		}
		rows = append(rows, pluginRow{
			ID:      m.ID,
			Name:    m.Name,
			Version: m.Version,
			Status:  "已停止",
			Port:    m.Port,
			Dir:     dir,
		})
	}
	return rows
}

// OpenPluginsDir 在资源管理器中打开插件目录。
func (s *GatewayService) OpenPluginsDir() {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if cfg == nil {
		return
	}
	if err := os.MkdirAll(cfg.PluginsDir, 0o755); err != nil {
		s.appendLog("创建插件目录失败：" + err.Error())
		return
	}
	openPath(cfg.PluginsDir)
}

// copyDir 递归复制目录。
func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("不是目录：%s", src)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		if err := os.WriteFile(d, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}