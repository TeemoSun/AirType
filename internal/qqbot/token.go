package qqbot

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// tokenSource 用 appId/clientSecret 换取并缓存 access_token。
// QQ 开放平台接口的鉴权头格式为 "Authorization: QQBot <access_token>"。
type tokenSource struct {
	appID        string
	clientSecret string

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func newTokenSource(appID, clientSecret string) *tokenSource {
	return &tokenSource{appID: appID, clientSecret: clientSecret}
}

// tokenURL / gatewayAPIURL 为 var 供测试替换为 httptest 服务。
var (
	tokenURL      = "https://bots.qq.com/app/getAppAccessToken"
	gatewayAPIURL = "https://api.sgroup.qq.com/gateway"
)

type tokenResp struct {
	AccessToken string `json:"access_token"`
	// 注意：QQ 返回的 expires_in 是字符串（"7200"），非数字
	ExpiresIn string `json:"expires_in"`
}

// get 返回有效 token，过期（或提前 90 秒内到期）时刷新。
func (t *tokenSource) get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Now().Before(t.expiresAt.Add(-90*time.Second)) {
		return t.token, nil
	}
	var resp tokenResp
	req := map[string]string{"appId": t.appID, "clientSecret": t.clientSecret}
	if err := postJSON(ctx, httpClient(), tokenURL, req, &resp); err != nil {
		return "", err
	}
	if resp.AccessToken == "" {
		return "", errEmptyToken
	}
	expiresIn, err := strconv.Atoi(strings.TrimSpace(resp.ExpiresIn))
	if err != nil || expiresIn <= 0 {
		expiresIn = 7200 // 解析失败按默认两小时兜底
	}
	t.token = resp.AccessToken
	t.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	return t.token, nil
}

// invalidate 在网关报 4004（token 无效）时强制下次重取。
func (t *tokenSource) invalidate() {
	t.mu.Lock()
	t.token = ""
	t.mu.Unlock()
}
