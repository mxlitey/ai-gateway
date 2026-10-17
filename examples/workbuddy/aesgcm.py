# -*- coding: utf-8 -*-
"""纯 Python 的 AES-256-GCM（只用于解开桌面端 at-rest 信封，不追求速度）。

为什么不引三方库：本插件与 trae 插件一样只依赖标准库，网关无需为它 pip 安装任何东西，
离线也能跑；AES-GCM 在这里只用于解密几个小字段，性能无关紧要。

AES 实现按 FIPS-197，GCM 按 NIST SP 800-38D（含 GHASH 与 96 位 IV 的 J0 约定）。
"""

# FIPS-197 的 S 盒
_SBOX = (
    0x63, 0x7c, 0x77, 0x7b, 0xf2, 0x6b, 0x6f, 0xc5, 0x30, 0x01, 0x67, 0x2b, 0xfe, 0xd7, 0xab, 0x76,
    0xca, 0x82, 0xc9, 0x7d, 0xfa, 0x59, 0x47, 0xf0, 0xad, 0xd4, 0xa2, 0xaf, 0x9c, 0xa4, 0x72, 0xc0,
    0xb7, 0xfd, 0x93, 0x26, 0x36, 0x3f, 0xf7, 0xcc, 0x34, 0xa5, 0xe5, 0xf1, 0x71, 0xd8, 0x31, 0x15,
    0x04, 0xc7, 0x23, 0xc3, 0x18, 0x96, 0x05, 0x9a, 0x07, 0x12, 0x80, 0xe2, 0xeb, 0x27, 0xb2, 0x75,
    0x09, 0x83, 0x2c, 0x1a, 0x1b, 0x6e, 0x5a, 0xa0, 0x52, 0x3b, 0xd6, 0xb3, 0x29, 0xe3, 0x2f, 0x84,
    0x53, 0xd1, 0x00, 0xed, 0x20, 0xfc, 0xb1, 0x5b, 0x6a, 0xcb, 0xbe, 0x39, 0x4a, 0x4c, 0x58, 0xcf,
    0xd0, 0xef, 0xaa, 0xfb, 0x43, 0x4d, 0x33, 0x85, 0x45, 0xf9, 0x02, 0x7f, 0x50, 0x3c, 0x9f, 0xa8,
    0x51, 0xa3, 0x40, 0x8f, 0x92, 0x9d, 0x38, 0xf5, 0xbc, 0xb6, 0xda, 0x21, 0x10, 0xff, 0xf3, 0xd2,
    0xcd, 0x0c, 0x13, 0xec, 0x5f, 0x97, 0x44, 0x17, 0xc4, 0xa7, 0x7e, 0x3d, 0x64, 0x5d, 0x19, 0x73,
    0x60, 0x81, 0x4f, 0xdc, 0x22, 0x2a, 0x90, 0x88, 0x46, 0xee, 0xb8, 0x14, 0xde, 0x5e, 0x0b, 0xdb,
    0xe0, 0x32, 0x3a, 0x0a, 0x49, 0x06, 0x24, 0x5c, 0xc2, 0xd3, 0xac, 0x62, 0x91, 0x95, 0xe4, 0x79,
    0xe7, 0xc8, 0x37, 0x6d, 0x8d, 0xd5, 0x4e, 0xa9, 0x6c, 0x56, 0xf4, 0xea, 0x65, 0x7a, 0xae, 0x08,
    0xba, 0x78, 0x25, 0x2e, 0x1c, 0xa6, 0xb4, 0xc6, 0xe8, 0xdd, 0x74, 0x1f, 0x4b, 0xbd, 0x8b, 0x8a,
    0x70, 0x3e, 0xb5, 0x66, 0x48, 0x03, 0xf6, 0x0e, 0x61, 0x35, 0x57, 0xb9, 0x86, 0xc1, 0x1d, 0x9e,
    0xe1, 0xf8, 0x98, 0x11, 0x69, 0xd9, 0x8e, 0x94, 0x9b, 0x1e, 0x87, 0xe9, 0xce, 0x55, 0x28, 0xdf,
    0x8c, 0xa1, 0x89, 0x0d, 0xbf, 0xe6, 0x42, 0x68, 0x41, 0x99, 0x2d, 0x0f, 0xb0, 0x54, 0xbb, 0x16,
)


def _xtime(a):
    return ((a << 1) ^ (0x1B if a & 0x80 else 0)) & 0xFF


def _gmul(a, b):
    """GF(2^8) 乘法。"""
    product = 0
    for _ in range(8):
        if b & 1:
            product ^= a
        a = _xtime(a)
        b >>= 1
    return product


def _expand_key(key):
    """AES-256 密钥扩展，返回 (轮密钥字表, 轮数)。"""
    nk = len(key) // 4
    nr = nk + 6
    words = [list(key[4 * i:4 * i + 4]) for i in range(nk)]
    rcon = 0x01
    for i in range(nk, 4 * (nr + 1)):
        temp = list(words[i - 1])
        if i % nk == 0:
            temp = temp[1:] + temp[:1]
            temp = [_SBOX[b] for b in temp]
            temp[0] ^= rcon
            rcon = _xtime(rcon)
        elif nk > 6 and i % nk == 4:
            temp = [_SBOX[b] for b in temp]
        words.append([words[i - nk][j] ^ temp[j] for j in range(4)])
    return words, nr


def _add_round_key(state, words, rnd):
    for col in range(4):
        word = words[4 * rnd + col]
        for row in range(4):
            state[4 * col + row] ^= word[row]


def _shift_rows(state):
    out = list(state)
    for row in range(4):
        for col in range(4):
            out[4 * col + row] = state[4 * ((col + row) % 4) + row]
    state[:] = out


def _mix_columns(state):
    for col in range(4):
        a0, a1, a2, a3 = state[4 * col:4 * col + 4]
        state[4 * col + 0] = _gmul(a0, 2) ^ _gmul(a1, 3) ^ a2 ^ a3
        state[4 * col + 1] = a0 ^ _gmul(a1, 2) ^ _gmul(a2, 3) ^ a3
        state[4 * col + 2] = a0 ^ a1 ^ _gmul(a2, 2) ^ _gmul(a3, 3)
        state[4 * col + 3] = _gmul(a0, 3) ^ a1 ^ a2 ^ _gmul(a3, 2)


def _encrypt_block(block, words, nr):
    """AES 分组加密（GCM 的 CTR 与 H 都只需要加密方向）。"""
    state = list(block)
    _add_round_key(state, words, 0)
    for rnd in range(1, nr):
        state[:] = [_SBOX[b] for b in state]
        _shift_rows(state)
        _mix_columns(state)
        _add_round_key(state, words, rnd)
    state[:] = [_SBOX[b] for b in state]
    _shift_rows(state)
    _add_round_key(state, words, nr)
    return bytes(state)


# GF(2^128) 的约简常量：x^128 + x^7 + x^2 + x + 1 的反射形式。
_GF_R = 0xE1000000000000000000000000000000


def _gf_mul128(x, y):
    """GF(2^128) 乘法（NIST SP 800-38D Algorithm 1）。"""
    z = 0
    v = y
    for i in range(128):
        if (x >> (127 - i)) & 1:
            z ^= v
        if v & 1:
            v = (v >> 1) ^ _GF_R
        else:
            v >>= 1
    return z


def _ghash(h_int, data):
    """GHASH：data 需已按 16 字节补齐。"""
    y = 0
    for offset in range(0, len(data), 16):
        y ^= int.from_bytes(data[offset:offset + 16], "big")
        y = _gf_mul128(y, h_int)
    return y


def _pad16(data):
    rest = len(data) % 16
    if rest == 0:
        return data
    return data + b"\x00" * (16 - rest)


class AESGCM:
    """AES-256-GCM 解密器（密钥固定 32 字节）。"""

    def __init__(self, key):
        if len(key) != 32:
            raise ValueError("AES-256 需要 32 字节密钥")
        self._words, self._nr = _expand_key(key)
        self._h = int.from_bytes(self._encrypt(b"\x00" * 16), "big")

    def _encrypt(self, block):
        return _encrypt_block(block, self._words, self._nr)

    def _j0(self, nonce):
        # 信封固定用 96 位 IV（parse_envelope 已校验），走标准简化路径 J0 = IV || 0^31 || 1
        if len(nonce) != 12:
            raise ValueError("本实现只支持 96 位 IV（信封约定）")
        return nonce + b"\x00\x00\x00\x01"

    def _tag(self, j0, aad, ciphertext):
        block = _pad16(aad) + _pad16(ciphertext)
        block += (len(aad) * 8).to_bytes(8, "big") + (len(ciphertext) * 8).to_bytes(8, "big")
        s = _ghash(self._h, block).to_bytes(16, "big")
        return bytes(a ^ b for a, b in zip(self._encrypt(j0), s))

    def decrypt(self, nonce, ciphertext, tag, aad=b""):
        """校验 tag 后返回明文；校验失败抛 ValueError。"""
        j0 = self._j0(nonce)
        if self._tag(j0, aad, ciphertext) != tag:
            raise ValueError("GCM 校验失败（密钥或 AAD 不匹配）")
        out = bytearray()
        counter = int.from_bytes(j0, "big")
        for offset in range(0, len(ciphertext), 16):
            # GCTR 的第一个计数器块是 inc32(J0)，不是 J0 本身
            counter = (counter & ~0xFFFFFFFF) | ((counter + 1) & 0xFFFFFFFF)
            keystream = self._encrypt(counter.to_bytes(16, "big"))
            chunk = ciphertext[offset:offset + 16]
            out.extend(a ^ b for a, b in zip(chunk, keystream))
        return bytes(out)
