package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mintToken 签发一个令牌，返回 token 与 payload。
func mintToken(t *testing.T, h *ImageHost, ttl time.Duration, maxMB int) (string, uploadTokenPayload) {
	t.Helper()
	token, payload, err := h.tokens.Mint(ttl, maxMB)
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}
	return token, payload
}

// uploadWithToken 走签名路径上传，返回响应与解析后的 id。
func uploadWithToken(t *testing.T, h *ImageHost, token string) (int, string, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/uploads/"+token, bytes.NewReader(testPNG))
	req.Header.Set("Content-Type", "image/png")
	req.SetPathValue("token", token)
	rec := httptest.NewRecorder()
	h.UploadByToken(rec, req)

	var out struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.ID, rec.Body.String()
}

// deleteOne 走签名路径单删一张，返回响应里的 deleted 与 revoked。
func deleteOne(t *testing.T, h *ImageHost, token, name string) (int, bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/v1/uploads/"+token+"/"+name, nil)
	req.SetPathValue("token", token)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	h.DeleteByPath(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("单删失败: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Deleted int  `json:"deleted"`
		Revoked bool `json:"revoked"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Deleted, out.Revoked
}

// TestUploadTokenMintRequiresKey 签发接口必须带有效 sk-。
func TestUploadTokenMintRequiresKey(t *testing.T) {
	h, _, _ := newTestImageHost(t, 1<<20)

	rec := httptest.NewRecorder()
	h.MintUploadToken(rec, httptest.NewRequest(http.MethodPost, "/v1/images/upload-tokens", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无密钥应 401，实际 %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/upload-tokens", strings.NewReader(`{"ttl_seconds":60,"max_mb":1}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	h.MintUploadToken(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("签发失败: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token      string `json:"token"`
		UploadURL  string `json:"upload_url"`
		ExpiresAt  int64  `json:"expires_at"`
		MaxMB      int    `json:"max_mb"`
		ExpiresIn  int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Token == "" || !strings.HasSuffix(out.UploadURL, out.Token) {
		t.Fatalf("响应不含可用令牌: %s", rec.Body.String())
	}
	if out.MaxMB != 1 || out.ExpiresIn <= 0 {
		t.Fatalf("max_mb / expires_in 不符: %+v", out)
	}
}

// TestUploadTokenTTLClamp 超过 24 小时的 TTL 截断到 24 小时。
func TestUploadTokenTTLClamp(t *testing.T) {
	h, _, _ := newTestImageHost(t, 1<<20)
	_, payload := mintToken(t, h, 72*time.Hour, 0)
	if got := payload.Exp - time.Now().Unix(); got > int64(uploadTokenTTLMax/time.Second) {
		t.Fatalf("TTL 未截断，实际 %d 秒", got)
	}
}

// TestUploadTokenMaxMBClampedToGlobal max_mb 超过全局上限（或未给）时按全局上限签发。
func TestUploadTokenMaxMBClampedToGlobal(t *testing.T) {
	h, _, _ := newTestImageHost(t, 1<<20) // 全局上限 1 MB
	for _, body := range []string{`{}`, `{"max_mb":99}`} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/images/upload-tokens", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer sk-test")
		h.MintUploadToken(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 签发失败: %d %s", body, rec.Code, rec.Body.String())
		}
		var out struct {
			MaxMB int `json:"max_mb"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.MaxMB != 1 {
			t.Fatalf("%s 的 max_mb 应回落到全局上限 1，实际 %d", body, out.MaxMB)
		}
	}
}

// TestUploadTokenRoundTrip 上传 → 读取 → 单删，还剩别的文件时令牌继续有效。
func TestUploadTokenRoundTrip(t *testing.T) {
	h, _, uploads := newTestImageHost(t, 1<<20)
	token, payload := mintToken(t, h, time.Minute, 0)

	code, id, body := uploadWithToken(t, h, token)
	if code != http.StatusOK {
		t.Fatalf("上传失败: %d %s", code, body)
	}
	if !strings.HasPrefix(id, payload.SID+"-") {
		t.Fatalf("文件名未带归属前缀: %q", id)
	}

	// 再传一张：单删第一张之后还剩一张，令牌不应被吊销
	if code, _, body := uploadWithToken(t, h, token); code != http.StatusOK {
		t.Fatalf("第二张上传失败: %d %s", code, body)
	}

	// 读取（公开）
	rec := httptest.NewRecorder()
	uploads.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/uploads/"+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("读取失败: %d", rec.Code)
	}

	// 单删
	deleted, revoked := deleteOne(t, h, token, id)
	if deleted != 1 || revoked {
		t.Fatalf("还剩文件时不应吊销，实际 deleted=%d revoked=%v", deleted, revoked)
	}
	// 被删的那张确实没了
	rec = httptest.NewRecorder()
	uploads.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/uploads/"+id, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("文件应已删除，实际 %d", rec.Code)
	}

	// 令牌仍可继续使用
	code, _, body = uploadWithToken(t, h, token)
	if code != http.StatusOK {
		t.Fatalf("单删后令牌应仍有效，实际 %d %s", code, body)
	}
}

// TestUploadTokenDeleteLastRevokes 逐张单删到最后一张时自动吊销，效果与整批删除一致。
func TestUploadTokenDeleteLastRevokes(t *testing.T) {
	h, _, uploads := newTestImageHost(t, 1<<20)
	token, payload := mintToken(t, h, time.Minute, 0)

	ids := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		code, id, body := uploadWithToken(t, h, token)
		if code != http.StatusOK {
			t.Fatalf("第 %d 张上传失败: %d %s", i, code, body)
		}
		ids = append(ids, id)
	}

	if _, revoked := deleteOne(t, h, token, ids[0]); revoked {
		t.Fatal("删第一张时不应吊销")
	}
	deleted, revoked := deleteOne(t, h, token, ids[1])
	if deleted != 1 || !revoked {
		t.Fatalf("删最后一张应吊销，实际 deleted=%d revoked=%v", deleted, revoked)
	}

	// 文件都没了
	for _, id := range ids {
		rec := httptest.NewRecorder()
		uploads.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/uploads/"+id, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("文件 %s 应已删除，实际 %d", id, rec.Code)
		}
	}

	// 令牌已吊销：再上传 401，再单删同一个令牌也是 401
	code, _, body := uploadWithToken(t, h, token)
	if code != http.StatusUnauthorized {
		t.Fatalf("吊销后上传应 401，实际 %d %s", code, body)
	}
	req := httptest.NewRequest(http.MethodDelete, "/v1/uploads/"+token+"/whatever", nil)
	req.SetPathValue("token", token)
	req.SetPathValue("name", "whatever")
	rec := httptest.NewRecorder()
	h.DeleteByPath(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("吊销后单删应 401，实际 %d", rec.Code)
	}
	_ = payload
}

// TestUploadTokenDeleteAllRevokes 整批删除后令牌立即吊销，剩余文件不会成孤儿。
func TestUploadTokenDeleteAllRevokes(t *testing.T) {
	h, _, uploads := newTestImageHost(t, 1<<20)
	token, payload := mintToken(t, h, time.Minute, 0)

	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		code, id, body := uploadWithToken(t, h, token)
		if code != http.StatusOK {
			t.Fatalf("第 %d 张上传失败: %d %s", i, code, body)
		}
		ids = append(ids, id)
	}
	// 先单删一张，验证不会提前吊销（还剩 2 张）
	if _, revoked := deleteOne(t, h, token, ids[0]); revoked {
		t.Fatal("还剩文件时不应吊销")
	}

	// 整批删除
	req := httptest.NewRequest(http.MethodDelete, "/v1/uploads/"+token, nil)
	req.SetPathValue("token", token)
	rec := httptest.NewRecorder()
	h.DeleteByPath(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("整批删除失败: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Deleted int  `json:"deleted"`
		Revoked bool `json:"revoked"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Deleted != 2 || !out.Revoked {
		t.Fatalf("应删除剩余 2 张并吊销，实际 %+v", out)
	}

	// 文件确实都没了
	for _, id := range ids[1:] {
		rec = httptest.NewRecorder()
		uploads.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/uploads/"+id, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("文件 %s 应已删除，实际 %d", id, rec.Code)
		}
	}

	// 令牌已吊销：再上传、再删除都应 401
	code, _, body := uploadWithToken(t, h, token)
	if code != http.StatusUnauthorized {
		t.Fatalf("吊销后上传应 401，实际 %d %s", code, body)
	}
	req = httptest.NewRequest(http.MethodDelete, "/v1/uploads/"+token, nil)
	req.SetPathValue("token", token)
	rec = httptest.NewRecorder()
	h.DeleteByPath(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("吊销后删除应 401，实际 %d", rec.Code)
	}
	_ = payload
}

// TestUploadTokenCannotDeleteOthers 令牌不能删别的令牌上传的文件。
func TestUploadTokenCannotDeleteOthers(t *testing.T) {
	h, _, _ := newTestImageHost(t, 1<<20)
	tokenA, _ := mintToken(t, h, time.Minute, 0)
	tokenB, _ := mintToken(t, h, time.Minute, 0)

	code, idB, body := uploadWithToken(t, h, tokenB)
	if code != http.StatusOK {
		t.Fatalf("上传失败: %d %s", code, body)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/uploads/"+tokenA+"/"+idB, nil)
	req.SetPathValue("token", tokenA)
	req.SetPathValue("name", idB)
	rec := httptest.NewRecorder()
	h.DeleteByPath(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("应 403，实际 %d %s", rec.Code, rec.Body.String())
	}
}

// TestUploadTokenCannotDeleteGeneratedImage 令牌不能删生图结果（那份在另一个目录）。
func TestUploadTokenCannotDeleteGeneratedImage(t *testing.T) {
	h, images, _ := newTestImageHost(t, 1<<20)
	token, payload := mintToken(t, h, time.Minute, 0)

	name, err := images.Save(testPNG, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/v1/images/files/"+name, nil)
	req.SetPathValue("token", token)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	h.DeleteByPath(rec, req)
	// 走的是 uploads 的归属校验，文件名不带 sid 前缀 → 403
	if rec.Code != http.StatusForbidden {
		t.Fatalf("应 403，实际 %d %s", rec.Code, rec.Body.String())
	}
	_ = payload
}

// TestUploadTokenExpired 过期令牌上传/删除均 401。
func TestUploadTokenExpired(t *testing.T) {
	h, _, _ := newTestImageHost(t, 1<<20)
	token, _ := mintToken(t, h, time.Minute, 0)

	// 直接改 payload 的 exp 再重新签名，模拟已过期但签名正确的令牌
	parts := strings.Split(token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var p uploadTokenPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	p.Exp = time.Now().Add(-time.Minute).Unix()
	raw, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := h.tokens.signingSecret()
	if err != nil {
		t.Fatal(err)
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	expired := body + "." + signBytes(body, secret)

	code, _, respBody := uploadWithToken(t, h, expired)
	if code != http.StatusUnauthorized {
		t.Fatalf("过期令牌应 401，实际 %d %s", code, respBody)
	}
	if !strings.Contains(respBody, "token_expired") {
		t.Fatalf("错误码应为 token_expired: %s", respBody)
	}
}

// TestUploadTokenTampered 篡改载荷即签名校验失败。
func TestUploadTokenTampered(t *testing.T) {
	h, _, _ := newTestImageHost(t, 1<<20)
	token, _ := mintToken(t, h, time.Minute, 0)
	parts := strings.Split(token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	raw[0] ^= 0xff
	tampered := base64.RawURLEncoding.EncodeToString(raw) + "." + parts[1]

	code, _, body := uploadWithToken(t, h, tampered)
	if code != http.StatusUnauthorized {
		t.Fatalf("篡改令牌应 401，实际 %d %s", code, body)
	}
	if !strings.Contains(body, "invalid_token") {
		t.Fatalf("错误码应为 invalid_token: %s", body)
	}
}

// TestUploadTokenMaxMBEnforced 令牌收紧的单张上限生效。
func TestUploadTokenMaxMBEnforced(t *testing.T) {
	h, _, _ := newTestImageHost(t, 1<<20)
	// 1MB 全局上限，令牌收紧到 0 表示沿用全局；这里用一个超大 body 触发全局上限
	big := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x01}, 1<<20)...)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/upload-tokens", strings.NewReader(`{"max_mb":1}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	h.MintUploadToken(rec, req)
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/uploads/"+out.Token, bytes.NewReader(big))
	req.SetPathValue("token", out.Token)
	rec = httptest.NewRecorder()
	h.UploadByToken(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应 413，实际 %d %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteFallsBackToSkPath 段值不是令牌时，DELETE /v1/uploads/{name} 仍按 sk- 鉴权工作。
func TestDeleteFallsBackToSkPath(t *testing.T) {
	h, _, _ := newTestImageHost(t, 1<<20)

	// 先用 sk- 直传一张（文件名无 sid 前缀）
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/uploads", bytes.NewReader(testPNG))
	req.Header.Set("Content-Type", "image/png")
	req.Header.Set("Authorization", "Bearer sk-test")
	h.Upload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("上传失败: %d %s", rec.Code, rec.Body.String())
	}
	var up struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}

	// 带 sk- 走旧入口删除
	req = httptest.NewRequest(http.MethodDelete, "/v1/uploads/"+up.ID, nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	req.SetPathValue("token", up.ID)
	rec = httptest.NewRecorder()
	h.DeleteByPath(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("旧入口删除失败: %d %s", rec.Code, rec.Body.String())
	}
}
