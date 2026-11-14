package mcp

import (
	"strings"
	"testing"
)

// TestInstructionsFor 验证 initialize 的说明书区分「聚合入口」与「单服务器直连」。
func TestInstructionsFor(t *testing.T) {
	aggregate := instructionsFor("", false)
	if !strings.Contains(aggregate, "tools/list") || !strings.Contains(aggregate, "structuredContent.error") {
		t.Fatalf("聚合入口说明书不完整: %s", aggregate)
	}

	single := instructionsFor("image-generation", false)
	if !strings.Contains(single, "image-generation") || !strings.Contains(single, "tools/call") {
		t.Fatalf("直连说明书不完整: %s", single)
	}

	if strings.Contains(aggregate, "/v1/images/uploads") {
		t.Fatalf("图床关闭时不应介绍上传接口: %s", aggregate)
	}
	withHost := instructionsFor("", true)
	if !strings.Contains(withHost, "/v1/images/uploads") || !strings.Contains(withHost, "/v1/images/host") {
		t.Fatalf("图床开启时缺少直传说明: %s", withHost)
	}
}
