// Package version 保存构建期注入的版本信息。
package version

var (
	// Version 版本号，构建时通过 -ldflags -X 注入
	Version = "dev"
	// Commit 构建对应的提交号
	Commit = "none"
	// BuildTime 构建时间
	BuildTime = "unknown"
)

// String 返回可读的版本描述。
func String() string {
	return Version + " (" + Commit + ", " + BuildTime + ")"
}