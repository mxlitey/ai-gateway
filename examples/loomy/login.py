# -*- coding: utf-8 -*-
"""Loomy 短信验证码登录（两步式）。

面板的 `fields` 只收集**一次**表单，而短信登录天然是两步：
    「发送验证码」→ 用户收短信 → 「提交验证码」
故声明**两个**登录方法：
    sms-code   只填手机号，发码后把 msgid 记在插件内存里（键 = 手机号）
    sms-login  填手机号 + 验证码，取回上一步的 msgid 完成登录
两步的 msgid 5 分钟内有效（与服务端的验证码 TTL 一致）。
"""

import threading
import time

from accounts import Credential
from upstream import UpstreamError, as_upstream_error
from util import mask_phone, new_state, now_millis

MSGID_TTL = 300


class LoginBroker:
    def __init__(self, plugin):
        self.plugin = plugin
        self.mu = threading.Lock()
        # phone → (msgid, created)
        self.pending = {}

    def send_code(self, body):
        phone = (body.get("phone") or "").strip()
        if not phone:
            return 400, {"error": "请填写手机号"}
        try:
            msgid = self.plugin.upstream.send_sms(phone)
        except UpstreamError as err:
            return err.status, {"error": err.msg}
        except Exception as exc:
            return 502, {"error": as_upstream_error(exc).msg}
        with self.mu:
            self.pending[phone] = (msgid, time.time())
        return 200, {
            "status": "ok",
            "message": "验证码已发送到 %s，请在「提交短信验证码」里填入它" % mask_phone(phone),
        }

    def login(self, body):
        phone = (body.get("phone") or "").strip()
        code = (body.get("code") or "").strip()
        if not phone or not code:
            return 400, {"error": "请填写手机号与验证码"}
        with self.mu:
            entry = self.pending.get(phone)
        if entry is None:
            return 400, {"error": "请先点「发送短信验证码」，且两步使用同一个手机号"}
        msgid, created = entry
        if time.time() - created > MSGID_TTL:
            with self.mu:
                self.pending.pop(phone, None)
            return 400, {"error": "验证码已过期，请重新发送"}
        try:
            result = self.plugin.upstream.login_sms(phone, code, msgid)
        except UpstreamError as err:
            return err.status, {"error": err.msg}
        except Exception as exc:
            return 502, {"error": as_upstream_error(exc).msg}

        with self.mu:
            self.pending.pop(phone, None)
        uid = result["userid"] or new_state()
        cred = Credential(
            uid=_safe_uid(uid),
            nickname=mask_phone(phone),
            phone=phone,
            token=result["session"],
            expires_at=now_millis() + int(self.plugin.eps.session_ttl_seconds) * 1000,
            source="sms",
        )
        saved, created_new = self.plugin.store.upsert(cred)
        return 200, {
            "status": "ok",
            "message": "%s账号 %s" % ("已新增" if created_new else "已更新", saved.name()),
            "account": {
                "id": saved.uid,
                "name": saved.name(),
                "site": "cn",
                "enabled": saved.usable(),
                "expires_at": int(saved.expires_at or 0),
            },
        }

    def close_all(self):
        with self.mu:
            self.pending.clear()


def _safe_uid(value):
    text = (value or "").strip()
    for ch in ("/", "\\", "?", "#", " "):
        text = text.replace(ch, "_")
    return "".join(ch for ch in text if ch.isprintable()) or new_state()
