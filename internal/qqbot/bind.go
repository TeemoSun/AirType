//go:build windows

package qqbot

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// 扫码绑定协议（逆向自 @tencent-connect/qqbot-connector 1.2.0）：
//  1. POST /lite/create_bind_task  {key: base64(32B随机)} → {data:{task_id}}
//  2. 二维码内容 = connectURL(task_id)；手机 QQ 扫码确认
//  3. POST /lite/poll_bind_result {task_id}（2s 轮询）
//     → {data:{status, bot_appid, bot_encrypt_secret, user_openid}}
//     status: 0 未扫 1 已扫待确认 2 完成 3 过期（过期则重新建任务换码）
//  4. bot_encrypt_secret 用第 1 步的 key 做 AES-256-GCM 解密得 AppSecret：
//     密文 base64 → iv=前12字节, tag=末16字节, 中间为密文本体

// bindHost 为 var 供测试替换为 httptest 服务。
var bindHost = "https://q.qq.com"

const bindTimeout = 10 * time.Second

// bindStatus 是 poll_bind_result 的状态机取值。
type bindStatus int

const (
	bindNone      bindStatus = 0
	bindPending   bindStatus = 1
	bindCompleted bindStatus = 2
	bindExpired   bindStatus = 3
)

type bindTask struct {
	taskID string
	key    string // base64 的 AES-256 密钥（本地生成，不发给服务端以外任何人）
}

type createBindTaskResp struct {
	RetCode int    `json:"retcode"`
	Msg     string `json:"msg"`
	Data    struct {
		TaskID string `json:"task_id"`
	} `json:"data"`
}

type pollBindResultResp struct {
	RetCode int    `json:"retcode"`
	Msg     string `json:"msg"`
	Data    struct {
		Status          int    `json:"status"`
		BotAppID        string `json:"bot_appid"`
		BotEncryptSecrt string `json:"bot_encrypt_secret"`
		UserOpenid      string `json:"user_openid"`
	} `json:"data"`
}

func postJSON(ctx context.Context, client *http.Client, rawURL string, req, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, bindTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
	if err != nil {
		return err
	}
	if httpResp.StatusCode != http.StatusOK {
		return fmt.Errorf("qqbot: HTTP %d from %s: %.200s", httpResp.StatusCode, rawURL, data)
	}
	return json.Unmarshal(data, resp)
}

// createBindTask 申请绑定任务（生成一次性 AES 密钥）。
func createBindTask(ctx context.Context, client *http.Client) (bindTask, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return bindTask{}, fmt.Errorf("qqbot: 生成绑定密钥失败: %w", err)
	}
	var resp createBindTaskResp
	req := map[string]string{"key": base64.StdEncoding.EncodeToString(key)}
	if err := postJSON(ctx, client, bindHost+"/lite/create_bind_task", req, &resp); err != nil {
		return bindTask{}, fmt.Errorf("qqbot: create_bind_task: %w", err)
	}
	if resp.RetCode != 0 || resp.Data.TaskID == "" {
		return bindTask{}, fmt.Errorf("qqbot: create_bind_task 失败: retcode=%d msg=%s", resp.RetCode, resp.Msg)
	}
	return bindTask{taskID: resp.Data.TaskID, key: base64.StdEncoding.EncodeToString(key)}, nil
}

// pollBindResult 查询一次绑定进度。
func pollBindResult(ctx context.Context, client *http.Client, taskID string) (pollBindResultResp, error) {
	var resp pollBindResultResp
	req := map[string]string{"task_id": taskID}
	if err := postJSON(ctx, client, bindHost+"/lite/poll_bind_result", req, &resp); err != nil {
		return pollBindResultResp{}, fmt.Errorf("qqbot: poll_bind_result: %w", err)
	}
	if resp.RetCode != 0 {
		return pollBindResultResp{}, fmt.Errorf("qqbot: poll_bind_result retcode=%d msg=%s", resp.RetCode, resp.Msg)
	}
	return resp, nil
}

// connectURL 生成二维码内容：手机 QQ 扫描后确认绑定。
// source 沿用官方 CLI 的 "openclaw"（connect.html 页面按该值渲染，保证兼容）。
func connectURL(taskID string) string {
	q := url.Values{}
	q.Set("task_id", taskID)
	q.Set("source", "openclaw")
	q.Set("_wv", "2")
	return bindHost + "/qqbot/openclaw/connect.html?" + q.Encode()
}

// decryptSecret 以绑定密钥解密 bot_encrypt_secret（AES-256-GCM，
// iv=前12字节，tag=末16字节）。与官方 Node 实现的字节布局一致。
func decryptSecret(encryptedB64, keyB64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return "", fmt.Errorf("qqbot: 绑定密钥非 base64: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(encryptedB64)
	if err != nil {
		return "", fmt.Errorf("qqbot: 密文非 base64: %w", err)
	}
	if len(data) < 12+16+1 || len(key) != 32 {
		return "", fmt.Errorf("qqbot: 密文/密钥长度异常（%d/%d）", len(data), len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv, tag, ct := data[:12], data[len(data)-16:], data[12:len(data)-16]
	plain, err := gcm.Open(nil, iv, append(ct, tag...), nil)
	if err != nil {
		return "", fmt.Errorf("qqbot: 解密 AppSecret 失败: %w", err)
	}
	return string(plain), nil
}
