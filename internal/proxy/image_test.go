package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/imagestore"
	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
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
	}, "Qwen/Qwen-Image-2.1-Turbo", nil, ImagePersist{})

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
	}, "img", nil, ImagePersist{})
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
	}, "pub-img", nil, ImagePersist{})
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
	res := GenerateImage(context.Background(), srv.Client(), ch, "k", map[string]any{"model": "img", "prompt": "x"}, "img", nil, ImagePersist{})
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
	}, "img", nil, ImagePersist{})
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
	}, "img", nil, ImagePersist{})
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
	}, "img", nil, ImagePersist{})
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

// TestImagePersistPrefersPublicBase 验证显式配置的对外地址优先于请求 Host。
func TestImagePersistPrefersPublicBase(t *testing.T) {
	images, err := imagestore.Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{images: images}
	h.SetPublicBaseURL("https://gw.example.com/")

	p := h.imagePersist(httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/v1/images/generations", nil))
	if p.BaseURL != "https://gw.example.com" {
		t.Fatalf("BaseURL = %q", p.BaseURL)
	}
	if got := p.url("abc.png"); got != "https://gw.example.com/v1/images/files/abc.png" {
		t.Fatalf("url = %q", got)
	}
}

// TestGenerateImagePersistsURL 验证上游图片会落盘，并替换为网关自己的地址。
func TestGenerateImagePersistsURL(t *testing.T) {
	const imageContent = "\x89PNG\r\n\x1a\n-gateway-image"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/images/generations":
			_, _ = w.Write([]byte(`{"data":[{"url":"` + srvURL(r) + `/img.png"}]}`))
		case "/img.png":
			_, _ = w.Write([]byte(imageContent))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	images, err := imagestore.Open(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	ch := &model.Channel{
		Name:        "sync",
		BaseURL:     srv.URL,
		ImageModels: []string{"img"},
		ImageGen:    model.ImageGenConfig{Mode: model.ImageGenSync},
	}
	res := GenerateImage(context.Background(), srv.Client(), ch, "k", map[string]any{
		"model": "img", "prompt": "x",
	}, "img", nil, ImagePersist{Store: images, BaseURL: "http://gateway.local"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, error = %s", res.StatusCode, res.Error)
	}

	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 {
		t.Fatalf("unexpected data: %s", res.Body)
	}
	url, _ := out.Data[0]["url"].(string)
	if !strings.HasPrefix(url, "http://gateway.local"+imagestore.URLPrefix) {
		t.Fatalf("url 未替换为网关地址: %q", url)
	}
	if _, ok := out.Data[0]["b64_json"]; ok {
		t.Fatalf("未请求 b64_json 时不应返回: %s", res.Body)
	}

	name := strings.TrimPrefix(url, "http://gateway.local"+imagestore.URLPrefix)
	stored, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != imageContent {
		t.Fatalf("落盘内容不一致: %q", stored)
	}
}

// TestGenerateImagePersistKeepsBase64 验证显式要求 b64_json 时同时返回 b64 与网关地址。
func TestGenerateImagePersistKeepsBase64(t *testing.T) {
	const imageContent = "raw-image-bytes"
	b64 := base64.StdEncoding.EncodeToString([]byte(imageContent))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"data:image/png;base64,` + b64 + `"}]}`))
	}))
	defer srv.Close()

	images, err := imagestore.Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ch := &model.Channel{
		Name:        "sync",
		BaseURL:     srv.URL,
		ImageModels: []string{"img"},
		ImageGen:    model.ImageGenConfig{Mode: model.ImageGenSync},
	}
	res := GenerateImage(context.Background(), srv.Client(), ch, "k", map[string]any{
		"model": "img", "prompt": "x", "response_format": "b64_json",
	}, "img", nil, ImagePersist{Store: images, BaseURL: "http://gateway.local/"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, error = %s", res.StatusCode, res.Error)
	}

	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 {
		t.Fatalf("unexpected data: %s", res.Body)
	}
	if out.Data[0]["b64_json"] != b64 {
		t.Fatalf("b64_json = %v", out.Data[0]["b64_json"])
	}
	url, _ := out.Data[0]["url"].(string)
	if !strings.HasPrefix(url, "http://gateway.local"+imagestore.URLPrefix) {
		t.Fatalf("缺少网关地址: %q", url)
	}
}

// TestShouldCoolImageFailure 验证参数类错误不冷却渠道，其余失败仍冷却。
func TestShouldCoolImageFailure(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusRequestEntityTooLarge,
		http.StatusUnsupportedMediaType,
		http.StatusUnprocessableEntity,
	} {
		if shouldCoolImageFailure(status) {
			t.Fatalf("status %d 不应冷却渠道", status)
		}
	}
	for _, status := range []int{
		0, // 网络错误
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		if !shouldCoolImageFailure(status) {
			t.Fatalf("status %d 应冷却渠道", status)
		}
	}
}

// TestImageJobAsyncLifecycle 验证 async=true 的作业提交、查询与结果返回。
func TestImageJobAsyncLifecycle(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"https://cdn.example.com/a.png"}]}`))
	}))
	defer upstream.Close()

	s, err := store.Open(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.CreateAPIKey(&model.APIKey{
		ID: "key-1", Name: "test", Key: "sk-test",
		ChannelScope: model.ScopeAll, Enabled: true, CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	if err := s.CreateChannel(&model.Channel{
		ID:          "ch-1",
		Name:        "sync",
		BaseURL:     upstream.URL,
		Keys:        []model.ChannelKey{{Key: "sk-up", Enabled: true}},
		Models:      []string{"img"},
		ImageModels: []string{"img"},
		ImageGen:    model.ImageGenConfig{Mode: model.ImageGenSync, Path: "/images/generations"},
		Enabled:     true,
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	h := New(s, nil)

	body := `{"model":"img","prompt":"x","async":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Handle(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", w.Code, w.Body.String())
	}

	var submitted struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &submitted); err != nil {
		t.Fatalf("unmarshal submit: %v", err)
	}
	if submitted.ID == "" || submitted.Status != store.ImageJobQueued {
		t.Fatalf("submitted = %+v", submitted)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		poll := httptest.NewRequest(http.MethodGet, "/v1/images/jobs/"+submitted.ID, nil)
		poll.SetPathValue("id", submitted.ID)
		poll.Header.Set("Authorization", "Bearer sk-test")
		pw := httptest.NewRecorder()
		h.HandleImageJob(pw, poll)
		if pw.Code != http.StatusOK {
			t.Fatalf("poll status = %d, body = %s", pw.Code, pw.Body.String())
		}
		var view struct {
			Status string           `json:"status"`
			Data   []map[string]any `json:"data"`
			Error  map[string]any   `json:"error"`
		}
		if err := json.Unmarshal(pw.Body.Bytes(), &view); err != nil {
			t.Fatalf("unmarshal poll: %v", err)
		}
		if view.Status == store.ImageJobSucceeded {
			if len(view.Data) != 1 || view.Data[0]["url"] != "https://cdn.example.com/a.png" {
				t.Fatalf("data = %s", pw.Body.String())
			}
			break
		}
		if view.Status == store.ImageJobFailed {
			t.Fatalf("job failed: %s", pw.Body.String())
		}
		if time.Now().After(deadline) {
			t.Fatalf("job not finished, last = %s", pw.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestImageJobCancel 验证取消后作业状态立即变为 canceled。
func TestImageJobCancel(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"https://cdn.example.com/a.png"}]}`))
	}))
	defer upstream.Close()
	defer close(release)

	s, err := store.Open(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	_ = s.CreateAPIKey(&model.APIKey{
		ID: "key-1", Name: "test", Key: "sk-test",
		ChannelScope: model.ScopeAll, Enabled: true, CreatedAt: time.Now().UnixMilli(),
	})
	_ = s.CreateChannel(&model.Channel{
		ID: "ch-1", Name: "sync", BaseURL: upstream.URL,
		Keys:   []model.ChannelKey{{Key: "sk-up", Enabled: true}},
		Models: []string{"img"}, ImageModels: []string{"img"},
		ImageGen: model.ImageGenConfig{Mode: model.ImageGenSync, Path: "/images/generations"},
		Enabled:  true,
	})

	h := New(s, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"img","prompt":"x","async":true}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Handle(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", w.Code, w.Body.String())
	}
	var submitted struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &submitted)

	cancelReq := httptest.NewRequest(http.MethodDelete, "/v1/images/jobs/"+submitted.ID, nil)
	cancelReq.SetPathValue("id", submitted.ID)
	cancelReq.Header.Set("Authorization", "Bearer sk-test")
	cw := httptest.NewRecorder()
	h.CancelImageJob(cw, cancelReq)
	if cw.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cw.Code, cw.Body.String())
	}
	if !strings.Contains(cw.Body.String(), `"status":"canceled"`) {
		t.Fatalf("cancel body = %s", cw.Body.String())
	}
}

// TestImageJobResumePolling 验证重启后能按已保存的 task_id 继续轮询。
func TestImageJobResumePolling(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks/task-1" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"task_status":"SUCCEED","output_images":["https://cdn.example.com/r.png"]}`))
	}))
	defer upstream.Close()

	s, err := store.Open(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	_ = s.CreateChannel(&model.Channel{
		ID: "ch-1", Name: "async", BaseURL: upstream.URL,
		Keys:   []model.ChannelKey{{Key: "sk-1", Enabled: true}},
		Models: []string{"img"}, ImageModels: []string{"img"},
		ImageGen: model.ImageGenConfig{
			Mode: model.ImageGenAsync, Path: "/images/generations", PollPath: "/tasks/{task_id}",
			PollIntervalMS: 20, TimeoutMS: 5000, SuccessStatus: "SUCCEED", FailedStatus: "FAILED",
		},
		Enabled: true,
	})

	now := time.Now()
	_ = s.CreateImageJob(&store.ImageJob{
		ID: "imgjob_resume", APIKeyID: "key-1", Model: "img",
		Status: store.ImageJobRunning, Stage: "polling",
		ChannelID: "ch-1", ChannelKey: "sk-1", UpstreamTaskID: "task-1",
		ResponseFormat: "url",
		CreatedAt:      now.UnixMilli(), UpdatedAt: now.UnixMilli(),
		ExpiresAt: now.Add(time.Hour).UnixMilli(),
	})

	h := New(s, nil)
	h.ResumeImageJobs()

	deadline := time.Now().Add(5 * time.Second)
	for {
		j, err := s.GetImageJob("imgjob_resume")
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if j.Status == store.ImageJobSucceeded {
			if !strings.Contains(string(j.Result), "https://cdn.example.com/r.png") {
				t.Fatalf("result = %s", j.Result)
			}
			return
		}
		if j.Status == store.ImageJobFailed {
			t.Fatalf("resume failed: %s", j.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("resume not finished, status = %s", j.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestGenerateImageAddsExpiresAt 验证落盘结果带上 expires_at（Unix 秒），与图床上传接口口径一致。
func TestGenerateImageAddsExpiresAt(t *testing.T) {
	bin, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/images/generations":
			_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"` + srvURL(r) + `/img.png"}]}`))
		case "/img.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(bin)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	imgStore, err := imagestore.Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatalf("open image store: %v", err)
	}
	ch := &model.Channel{
		Name:        "sync",
		BaseURL:     srv.URL,
		ImageModels: []string{"img"},
		ImageGen:    model.ImageGenConfig{Mode: model.ImageGenSync},
	}
	res := GenerateImage(context.Background(), srv.Client(), ch, "k", map[string]any{
		"model": "img", "prompt": "x",
	}, "img", nil, ImagePersist{Store: imgStore, BaseURL: "https://gw.example"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, error = %s", res.StatusCode, res.Error)
	}

	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 {
		t.Fatalf("data = %s", res.Body)
	}
	if got, _ := out.Data[0]["url"].(string); !strings.HasPrefix(got, "https://gw.example/v1/images/files/") {
		t.Fatalf("url = %q", got)
	}
	expires, _ := out.Data[0]["expires_at"].(float64)
	now := time.Now().Unix()
	if int64(expires) < now+50*60 || int64(expires) > now+70*60 {
		t.Fatalf("expires_at = %v, now = %d", expires, now)
	}
}

// TestGenerateImageAsyncCanceled 验证作业上下文被取消时返回 Canceled，而不是“轮询超时 5m”。
func TestGenerateImageAsyncCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/images/generations":
			_, _ = w.Write([]byte(`{"task_id":"t-cancel"}`))
		case "/tasks/t-cancel":
			_, _ = w.Write([]byte(`{"task_status":"RUNNING"}`))
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
			PollIntervalMS: 20,
			TimeoutMS:      5000,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()

	res := GenerateImage(ctx, srv.Client(), ch, "k", map[string]any{
		"model": "img", "prompt": "x",
	}, "img", nil, ImagePersist{})
	if !res.Canceled {
		t.Fatalf("canceled = false, status = %d, error = %s", res.StatusCode, res.Error)
	}
}
