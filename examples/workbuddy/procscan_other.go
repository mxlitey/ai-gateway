//go:build !windows

package main

import "errors"

// findProtectorKey 在非 Windows 平台不可用。
func findProtectorKey(string) ([]byte, error) {
	return nil, errors.New("读取本机桌面端登录态目前只支持 Windows")
}

func probeProcesses(string) ([]string, error) {
	return nil, errors.New("读取本机桌面端登录态目前只支持 Windows")
}