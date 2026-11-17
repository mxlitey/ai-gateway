package proxy

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/routing"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

func testTarget(id, name string) routing.Target {
	return routing.Target{
		Channel:       &model.Channel{ID: id, Name: name},
		UpstreamModel: name + "-up",
		Key:           "sk-test-key-0123456789",
		PublicModel:   "m",
	}
}

// TestRecorderPerAttemptRecoversFailover 验证调用明细按「实际请求上游的次数」计数：
// 故障转移的每次尝试各落一条，而不是合成一条。
func TestRecorderPerAttemptRecoversFailover(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	from := time.Now().Add(-time.Minute).UnixMilli()
	to := time.Now().Add(time.Minute).UnixMilli()

	rec := newRecorder(st, nil, &model.APIKey{ID: "k1", Name: "key1"}, "chat", false, "m")

	// 第一次尝试：上游返回 502
	rec.Attempt()
	rec.Served(testTarget("ch-a", "A"))
	rec.Finish(http.StatusBadGateway, "HTTP 502")

	// 故障转移：第二次尝试成功
	rec2 := rec.nextAttempt(testTarget("ch-b", "B"))
	rec2.Attempt()
	rec2.Served(testTarget("ch-b", "B"))
	rec2.Finish(http.StatusOK, "")
	rec2.Commit()

	// 等待异步写入
	time.Sleep(200 * time.Millisecond)

	items, err := st.ListRequestLogs(store.RequestLogFilter{FromMs: from, ToMs: to, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("期望 2 条上游记录，实际 %d 条", len(items))
	}
	// 两条记录可能落在同一毫秒，按渠道名查找而不是依赖排序
	byChannel := map[string]model.RequestLog{}
	for _, it := range items {
		byChannel[it.ChannelName] = it
	}
	if a := byChannel["A"]; a.Success || a.Status != http.StatusBadGateway {
		t.Fatalf("A 应为失败的 502，实际 %+v", a)
	}
	if b := byChannel["B"]; !b.Success || b.Status != http.StatusOK {
		t.Fatalf("B 应为成功，实际 %+v", b)
	}

	sum, err := st.UsageSummary(from, to, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Requests != 2 || sum.SuccessRequests != 1 || sum.FailedRequests != 1 {
		t.Fatalf("汇总不符：requests=%d success=%d failed=%d",
			sum.Requests, sum.SuccessRequests, sum.FailedRequests)
	}
}

// TestRecordUsageSkipsGatewayRejection 验证没有请求上游的网关级拒绝不进入调用明细。
func TestRecordUsageSkipsGatewayRejection(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	from := time.Now().Add(-time.Minute).UnixMilli()
	to := time.Now().Add(time.Minute).UnixMilli()

	rec := newRecorder(st, nil, &model.APIKey{ID: "k1"}, "chat", false, "m")
	rec.Finish(http.StatusServiceUnavailable, "No available channel for model: m")
	rec.Commit()

	time.Sleep(200 * time.Millisecond)

	sum, err := st.UsageSummary(from, to, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Requests != 0 {
		t.Fatalf("网关级拒绝不应计入调用明细，实际 requests=%d", sum.Requests)
	}
}
