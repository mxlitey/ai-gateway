// Package web 内嵌前端构建产物。
//
// 构建流程会先把前端产物输出到 web/dist，再编译 Go 二进制，
// 因此运行期不需要任何外部前端文件。
package web

import "embed"

//go:embed all:dist
var Dist embed.FS