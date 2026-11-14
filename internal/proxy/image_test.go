package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

// TestGenerateImageAsyncModelScope 验证异步任务提交、带任务类型头轮询、URL 结果转换。
func TestGenerateImageAsyncModelScope(t *testing.T) {
	var submitCalls, pollCalls int32
	var submitHeader, pollHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/images/generations":
			atomic.AddInt32(&submitCalls, 1)
			submitHeader = r.Header.Get("X-ModelScope-Async-Mode")
			if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
				t.Errorf("authorization = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"task_id":"task-1","task_status":"PENDING"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/task-1":
			atomic.AddInt32(&pollCalls, 1)
			pollHeader = r.Header.Get("X-ModelScope-Task-Type")
			w.Header().Set("Content-Type", "application/json")
			if pollCalls == 1 {
				_, _ = w.Write([]byte(`{"task_id":"task-1","task_status":"RUNNING"}`))
				return
			}
			_, _ = w.Write([]byte(`{"task_id":"task-1","task_status":"SUCCEED","output_images":["https://cdn.example.com/a.png"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ch := &model.Channel{
		Name:        "modelscope",
		BaseURL:     srv.URL,
		ImageModels: []string{"Qwen/Qwen-Image-2.1-Turbo"},
		ImageGen: model.ImageGenConfig{
			Mode:           model.ImageGenAsync,
			SubmitHeaders:  []model.Header{{Name: "X-ModelScope-Async-Mode", Value: "true"}},
			PollHeaders:    []model.Header{{Name: "X-ModelScope-Task-Type", Value: "image_generation"}},
			PollIntervalMS: 100,
			TimeoutMS:      5000,
		},
	}

	res := GenerateImage(context.Background(), srv.Client(), ch, "sk-test", map[string]any{
		"model":  "Qwen/Qwen-Image-2.1-Turbo",
		"prompt": "a cat",
		"n":      1,
	}, "Qwen/Qwen-Image-2.1-Turbo", nil)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, error = %s", res.StatusCode, res.Error)
	}
	if submitCalls != 1 || pollCalls < 2 {
		t.Fatalf("submitCalls=%d pollCalls=%d", submitCalls, pollCalls)
	}
	if submitHeader != "true" {
		t.Fatalf("submit header = %q", submitHeader)
	}
	if pollHeader != "image_generation" {
		t.Fatalf("poll header = %q", pollHeader)
	}

	var out struct {
		Created int64            `json:"created"`
		Data    []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Data) != 1 || out.Data[0]["url"] != "https://cdn.example.com/a.png" {
		t.Fatalf("unexpected data: %s", res.Body)
	}
}

// TestGenerateImageAsyncB64 验证 response_format=b64_json 时服务端下载并编码。
func TestGenerateImageAsyncB64(t *testing.T) {
	const imageContent = "fake-png-bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/images/generations":
			_, _ = w.Write([]byte(`{"task_id":"t2"}`))
		case "/tasks/t2":
			_, _ = w.Write([]byte(`{"task_status":"SUCCEED","output_images":["` + srvURL(r) + `/img.png"]}`))
		case "/img.png":
			_, _ = w.Write([]byte(imageContent))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ch := &model.Channel{
		Name:        "async",
		BaseURL:     srv.URL,
		ImageModels: []string{"img"},
		ImageGen: model.ImageGenConfig{
			Mode:           model.ImageGenAsync,
			PollIntervalMS: 100,
			TimeoutMS:      5000,
		},
	}
	res := GenerateImage(context.Background(), srv.Client(), ch, "k", map[string]any{
		"model":           "img",
		"prompt":          "x",
		"response_format": "b64_json",
	}, "img", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, error = %s", res.StatusCode, res.Error)
	}
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString([]byte(imageContent))
	if len(out.Data) != 1 || out.Data[0]["b64_json"] != want {
		t.Fatalf("unexpected b64 data: %s", res.Body)
	}
}

// TestGenerateImageSyncOpenAIPassthrough 验证同步型服务商的标准 OpenAI 响应可直接透传。
func TestGenerateImageSyncOpenAIPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "upstream-img" {
			t.Errorf("upstream model = %v", body["model"])
		}
		_, _ = w.Write([]byte(`{"created":123,"data":[{"url":"https://x/y.png"}]}`))
	}))
	defer srv.Close()

	ch := &model.Channel{
		Name:        "sync",
		BaseURL:     srv.URL,
		ModelMap:    map[string]string{"pub-img": "upstream-img"},
		ImageModels: []string{"pub-img"},
		ImageGen:    model.ImageGenConfig{Mode: model.ImageGenSync},
	}
	res := GenerateImage(context.Background(), srv.Client(), ch, "k", map[string]any{
		"model":  "pub-img",
		"prompt": "x",
	}, "pub-img", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, error = %s", res.StatusCode, res.Error)
	}
	if !strings.Contains(string(res.Body), `"https://x/y.png"`) {
		t.Fatalf("body = %s", res.Body)
	}
}

// TestGenerateImageTaskFailed 验证异步任务失败时返回明确错误。
func TestGenerateImageTaskFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/images/generations" {
			_, _ = w.Write([]byte(`{"task_id":"t3"}`))
			return
		}
		_, _ = w.Write([]byte(`{"task_status":"FAILED","error":{"message":"content rejected"}}`))
	}))
	defer srv.Close()

	ch := &model.Channel{
		Name:        "async",
		BaseURL:     srv.URL,
		ImageModels: []string{"img"},
		ImageGen: model.ImageGenConfig{
			Mode:           model.ImageGenAsync,
			PollIntervalMS: 100,
			TimeoutMS:      5000,
		},
	}
	res := GenerateImage(context.Background(), srv.Client(), ch, "k", map[string]any{"model": "img", "prompt": "x"}, "img", nil)
	if res.StatusCode == http.StatusOK {
		t.Fatalf("expected failure, got body %s", res.Body)
	}
	if !strings.Contains(res.Error, "content rejected") {
		t.Fatalf("error = %s", res.Error)
	}
}

func srvURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// TestGenerateImageSyncTimeout 验证同步生图按渠道配置的超时终止，并返回 504。
func TestGenerateImageSyncTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":[{"url":"https://x/y.png"}]}`))
	}))
	defer srv.Close()

	ch := &model.Channel{
		Name:        "sync",
		BaseURL:     srv.URL,
		ImageModels: []string{"img"},
		// Normalized 会把小于 1 秒的值提升到 1 秒，这里用 1 秒并让上游慢 1.2 秒
		ImageGen: model.ImageGenConfig{Mode: model.ImageGenSync, SyncTimeoutMS: 1000},
	}
	res := GenerateImage(context.Background(), srv.Client(), ch, "k", map[string]any{
		"model": "img", "prompt": "x",
	}, "img", nil)
	if res.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, body = %s", res.StatusCode, res.Body)
	}
	if !strings.Contains(res.Error, "超时") {
		t.Fatalf("error = %s", res.Error)
	}
}

// TestGenerateImageEditMapping 验证参考图分流到编辑接口，并按配置重塑字段与参数。
func TestGenerateImageEditMapping(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/edits" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"data":[{"url":"https://x/e.png"}]}`))
	}))
	defer srv.Close()

	ch := &model.Channel{
		Name:        "sync",
		BaseURL:     srv.URL,
		ImageModels: []string{"img"},
		ImageGen: model.ImageGenConfig{
			Mode:               model.ImageGenSync,
			EditPath:           "/images/edits",
			ReferenceField:     "images",
			ReferenceShape:     model.ImageRefImageURLObjectArray,
			ParamMap:           map[string]string{"size": "image_size"},
			StaticParams:       map[string]any{"watermark": false},
			DropParams:         []string{"negative_prompt"},
			ResponseFormatMode: model.ImageFormatURL,
		},
	}
	res := GenerateImage(context.Background(), srv.Client(), ch, "k", map[string]any{
		"model":           "img",
		"prompt":          "x",
		"size":            "1024x1024",
		"image_url":       []any{"https://a/1.png"},
		"negative_prompt": "blur",
	}, "img", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, error = %s", res.StatusCode, res.Error)
	}

	if _, ok := got["image_url"]; ok {
		t.Fatalf("image_url 应被移到编辑字段: %v", got)
	}
	list, _ := got["images"].([]any)
	if len(list) != 1 {
		t.Fatalf("images = %v", got["images"])
	}
	first, _ := list[0].(map[string]any)
	if first["image_url"] != "https://a/1.png" {
		t.Fatalf("images[0] = %v", first)
	}
	if got["image_size"] != "1024x1024" {
		t.Fatalf("param_map 未生效: %v", got)
	}
	if _, ok := got["size"]; ok {
		t.Fatalf("原参数名应被删除: %v", got)
	}
	if _, ok := got["negative_prompt"]; ok {
		t.Fatalf("drop_params 未生效: %v", got)
	}
	if got["watermark"] != false {
		t.Fatalf("static_params 未生效: %v", got)
	}
	if got["response_format"] != "url" {
		t.Fatalf("response_format 策略未生效: %v", got)
	}
}

// TestGenerateImageNestedImagesPath 验证图片字段支持嵌套路径写法。
func TestGenerateImageNestedImagesPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"images":[{"url":"https://x/a.png"},{"url":"https://x/b.png"}]}}`))
	}))
	defer srv.Close()

	ch := &model.Channel{
		Name:        "sync",
		BaseURL:     srv.URL,
		ImageModels: []string{"img"},
		ImageGen: model.ImageGenConfig{
			Mode:        model.ImageGenSync,
			ImagesField: "result.images[].url",
		},
	}
	res := GenerateImage(context.Background(), srv.Client(), ch, "k", map[string]any{
		"model": "img", "prompt": "x",
	}, "img", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, error = %s", res.StatusCode, res.Error)
	}
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 2 || out.Data[1]["url"] != "https://x/b.png" {
		t.Fatalf("unexpected data: %s", res.Body)
	}
}
