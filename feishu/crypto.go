package feishu

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

// Decrypt 解密飞书事件回调的加密体。飞书加密方案（开放平台文档）：
// key = SHA-256(Encrypt Key)，AES-256-CBC，IV 为密文前 16 字节，PKCS7 填充。
func Decrypt(encryptKey, cipherB64 string) ([]byte, error) {
	if encryptKey == "" {
		return nil, errors.New("encrypt key not configured")
	}
	data, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	if len(data) < aes.BlockSize || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext too short or not block-aligned")
	}
	key := sha256.Sum256([]byte(encryptKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	iv := data[:aes.BlockSize]
	plain := make([]byte, len(data)-aes.BlockSize)
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, data[aes.BlockSize:])
	// 去 PKCS7 填充。
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(plain) {
		return nil, errors.New("bad PKCS7 padding")
	}
	return plain[:len(plain)-pad], nil
}

// Encrypt Decrypt 的逆过程，仅供测试构造加密事件。
func Encrypt(encryptKey string, plain []byte) (string, error) {
	key := sha256.Sum256([]byte(encryptKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := make([]byte, len(plain)+pad)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	out := make([]byte, aes.BlockSize+len(padded))
	iv := key[:aes.BlockSize]
	copy(out, iv)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out[aes.BlockSize:], padded)
	return base64.StdEncoding.EncodeToString(out), nil
}
