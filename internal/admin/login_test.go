package admin

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/czg95/ai-gateway-v2/internal/store"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st)
}

// 连续失败超过免费次数后必须进入退避，且提示可辨识（不是普通密码错误）。
func TestLoginBackoffAfterRepeatedFailures(t *testing.T) {
	svc := newTestService(t)
	if err := svc.SetPassword("correct-password"); err != nil {
		t.Fatalf("设置密码失败: %v", err)
	}

	for i := 0; i < loginFreeAttempts; i++ {
		token, ok, err := svc.Login("wrong-password", false)
		if err != nil {
			t.Fatalf("第 %d 次失败不应返回错误（只是密码不对）: %v", i+1, err)
		}
		if ok || token != "" {
			t.Fatalf("第 %d 次应登录失败", i+1)
		}
	}

	// 第 4 次失败开始退避，此后即使密码正确也要被拦下
	if _, _, err := svc.Login("wrong-password", false); err != nil {
		t.Fatalf("触发退避的那次应返回 nil 错误，实际 %v", err)
	}
	if _, _, err := svc.Login("correct-password", false); !errors.Is(err, ErrLoginThrottled) {
		t.Fatalf("锁定期间应以 ErrLoginThrottled 拒绝，实际 %v", err)
	}
}

// 登录成功必须清零计数：一次误输不应影响后续正常登录。
func TestLoginSuccessResetsFailures(t *testing.T) {
	svc := newTestService(t)
	if err := svc.SetPassword("correct-password"); err != nil {
		t.Fatalf("设置密码失败: %v", err)
	}
	for i := 0; i < loginFreeAttempts; i++ {
		_, _, _ = svc.Login("wrong-password", false)
	}
	if _, ok, err := svc.Login("correct-password", false); err != nil || !ok {
		t.Fatalf("免费次数内应允许登录成功: ok=%v err=%v", ok, err)
	}
	for i := 0; i < loginFreeAttempts; i++ {
		if _, ok, _ := svc.Login("wrong-password", false); ok {
			t.Fatal("错误密码不应登录成功")
		}
	}
	if _, ok, err := svc.Login("correct-password", false); err != nil || !ok {
		t.Fatalf("计数已清零，应允许登录: ok=%v err=%v", ok, err)
	}
}

// 改密必须轮换会话密钥，使旧 Token 失效。
func TestSetPasswordRotatesSessionSecret(t *testing.T) {
	svc := newTestService(t)
	if err := svc.SetPassword("first-password"); err != nil {
		t.Fatalf("设置密码失败: %v", err)
	}
	oldToken, ok, err := svc.Login("first-password", false)
	if err != nil || !ok {
		t.Fatalf("首次登录失败: ok=%v err=%v", ok, err)
	}

	if err := svc.SetPassword("second-password"); err != nil {
		t.Fatalf("改密失败: %v", err)
	}

	secret, err := svc.sessionSecret()
	if err != nil {
		t.Fatalf("读取会话密钥失败: %v", err)
	}
	if VerifyToken(oldToken, secret) {
		t.Fatal("改密后旧 Token 应失效")
	}
	if _, ok, err := svc.Login("second-password", false); err != nil || !ok {
		t.Fatalf("应能用新密码登录: ok=%v err=%v", ok, err)
	}
}
