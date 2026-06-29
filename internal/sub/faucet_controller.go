package sub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/skip2/go-qrcode"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	linkutil "github.com/mhsanaei/3x-ui/v3/internal/util/link"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const faucetCookieName = "xui_faucet_claim"

type FaucetController struct {
	path           string
	settingService service.SettingService
	inboundService service.InboundService
	clientService  service.ClientService
	xrayService    service.XrayService
	subService     *SubService
}

type faucetConfig struct {
	Path              string
	InboundIDs        []int
	ClientFlow        string
	TrafficBytes      int64
	ExpireHours       int
	LimitIP           int
	IpCooldownMinutes int
	IpDailyLimit      int
	GlobalDailyLimit  int
	TurnstileSiteKey  string
	TurnstileSecret   string
}

type faucetPageData struct {
	ClaimAction string
	SiteKey     string
	Error       string
	LinksText   string
	Links       []faucetShareItem
	ExpiresAt   string
}

type faucetShareItem struct {
	Link       string
	QRDataURI  template.URL
	ConfigText string
}

func NewFaucetController(g *gin.RouterGroup, path string) *FaucetController {
	a := &FaucetController{
		path:       normalizeFaucetPath(path),
		subService: NewSubService(""),
	}
	a.initRouter(g)
	return a
}

func (a *FaucetController) initRouter(g *gin.RouterGroup) {
	group := g.Group(a.path)
	group.GET("", a.page)
	group.POST("claim", a.claim)
}

func (a *FaucetController) page(c *gin.Context) {
	cfg, err := a.loadConfig()
	if err != nil {
		a.render(c, http.StatusInternalServerError, faucetPageData{Error: err.Error()})
		return
	}
	a.render(c, http.StatusOK, faucetPageData{
		ClaimAction: cfg.Path + "claim",
		SiteKey:     cfg.TurnstileSiteKey,
	})
}

func (a *FaucetController) claim(c *gin.Context) {
	cfg, err := a.loadConfig()
	if err != nil {
		a.renderClaimError(c, http.StatusInternalServerError, cfg, err)
		return
	}
	if len(cfg.InboundIDs) == 0 {
		a.renderClaimError(c, http.StatusBadRequest, cfg, common.NewError("no faucet inbounds configured"))
		return
	}

	ip := a.remoteIP(c)
	if err := a.verifyTurnstile(c, cfg, ip); err != nil {
		a.renderClaimError(c, http.StatusForbidden, cfg, err)
		return
	}

	if result, ok, err := a.existingClaim(c, cfg); ok || err != nil {
		if err != nil {
			a.renderClaimError(c, http.StatusInternalServerError, cfg, err)
			return
		}
		a.render(c, http.StatusOK, result)
		return
	}

	ipHash, err := a.hashText("ip:" + ip)
	if err != nil {
		a.renderClaimError(c, http.StatusInternalServerError, cfg, err)
		return
	}
	if err := a.checkLimits(cfg, ipHash); err != nil {
		a.renderClaimError(c, http.StatusTooManyRequests, cfg, err)
		return
	}

	email := ""
	expiresAt := time.Now().Add(time.Duration(cfg.ExpireHours) * time.Hour).UnixMilli()
	for i := 0; i < 3; i++ {
		email = "faucet-" + random.NumLower(16)
		needRestart, createErr := a.clientService.CreateFaucet(&a.inboundService, &service.ClientCreatePayload{
			Client: model.Client{
				Email:      email,
				Enable:     true,
				Flow:       cfg.ClientFlow,
				TotalGB:    cfg.TrafficBytes,
				ExpiryTime: expiresAt,
				LimitIP:    cfg.LimitIP,
			},
			InboundIds: cfg.InboundIDs,
		})
		if createErr == nil {
			if needRestart {
				if a.xrayService.IsXrayRunning() {
					if restartErr := a.xrayService.RestartXray(false); restartErr != nil {
						a.xrayService.SetToNeedRestart()
					}
				} else {
					a.xrayService.SetToNeedRestart()
				}
			}
			break
		}
		if !strings.Contains(createErr.Error(), "email already in use") || i == 2 {
			a.renderClaimError(c, http.StatusInternalServerError, cfg, createErr)
			return
		}
	}

	links, err := a.inboundService.GetAllClientLinks(a.linkHost(c), email)
	if err != nil {
		a.renderClaimError(c, http.StatusInternalServerError, cfg, err)
		return
	}
	if len(links) == 0 {
		a.renderClaimError(c, http.StatusInternalServerError, cfg, common.NewError("no share links available"))
		return
	}

	token := random.Seq(48)
	tokenHash, err := a.hashText("token:" + token)
	if err != nil {
		a.renderClaimError(c, http.StatusInternalServerError, cfg, err)
		return
	}
	if err := database.GetDB().Create(&model.FaucetClaim{
		TokenHash:   tokenHash,
		IPHash:      ipHash,
		ClientEmail: email,
		ExpiresAt:   expiresAt,
	}).Error; err != nil {
		a.renderClaimError(c, http.StatusInternalServerError, cfg, err)
		return
	}

	a.setClaimCookie(c, cfg, token, expiresAt)
	a.render(c, http.StatusOK, faucetPageData{
		ClaimAction: cfg.Path + "claim",
		SiteKey:     cfg.TurnstileSiteKey,
		LinksText:   strings.Join(links, "\n"),
		ExpiresAt:   time.UnixMilli(expiresAt).Format(time.RFC3339),
	})
}

func (a *FaucetController) existingClaim(c *gin.Context, cfg faucetConfig) (faucetPageData, bool, error) {
	token, err := c.Cookie(faucetCookieName)
	if err != nil || strings.TrimSpace(token) == "" {
		return faucetPageData{}, false, nil
	}
	tokenHash, err := a.hashText("token:" + token)
	if err != nil {
		return faucetPageData{}, false, err
	}
	var claim model.FaucetClaim
	err = database.GetDB().
		Where("token_hash = ? AND expires_at > ?", tokenHash, time.Now().UnixMilli()).
		First(&claim).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return faucetPageData{}, false, nil
	}
	if err != nil {
		return faucetPageData{}, false, err
	}
	links, err := a.inboundService.GetAllClientLinks(a.linkHost(c), claim.ClientEmail)
	if err != nil {
		return faucetPageData{}, false, err
	}
	if len(links) == 0 {
		return faucetPageData{}, false, common.NewError("no share links available")
	}
	return faucetPageData{
		ClaimAction: cfg.Path + "claim",
		SiteKey:     cfg.TurnstileSiteKey,
		LinksText:   strings.Join(links, "\n"),
		ExpiresAt:   time.UnixMilli(claim.ExpiresAt).Format(time.RFC3339),
	}, true, nil
}

func (a *FaucetController) checkLimits(cfg faucetConfig, ipHash string) error {
	now := time.Now().UnixMilli()
	if cfg.IpCooldownMinutes > 0 {
		var count int64
		since := now - int64(cfg.IpCooldownMinutes)*int64(time.Minute/time.Millisecond)
		if err := database.GetDB().Model(&model.FaucetClaim{}).
			Where("ip_hash = ? AND created_at >= ?", ipHash, since).
			Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return common.NewError("please wait before claiming again")
		}
	}
	if cfg.IpDailyLimit > 0 {
		var count int64
		if err := database.GetDB().Model(&model.FaucetClaim{}).
			Where("ip_hash = ? AND created_at >= ?", ipHash, now-int64(24*time.Hour/time.Millisecond)).
			Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(cfg.IpDailyLimit) {
			return common.NewError("daily claim limit reached")
		}
	}
	if cfg.GlobalDailyLimit > 0 {
		var count int64
		if err := database.GetDB().Model(&model.FaucetClaim{}).
			Where("created_at >= ?", now-int64(24*time.Hour/time.Millisecond)).
			Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(cfg.GlobalDailyLimit) {
			return common.NewError("global daily claim limit reached")
		}
	}
	return nil
}

func (a *FaucetController) loadConfig() (faucetConfig, error) {
	path, err := a.settingService.GetFaucetPath()
	if err != nil {
		return faucetConfig{}, err
	}
	rawInboundIDs, err := a.settingService.GetFaucetInboundIds()
	if err != nil {
		return faucetConfig{}, err
	}
	clientFlow, err := a.settingService.GetFaucetClientFlow()
	if err != nil {
		return faucetConfig{}, err
	}
	trafficMB, err := a.settingService.GetFaucetTrafficMB()
	if err != nil {
		return faucetConfig{}, err
	}
	expireHours, err := a.settingService.GetFaucetExpireHours()
	if err != nil {
		return faucetConfig{}, err
	}
	limitIP, err := a.settingService.GetFaucetLimitIP()
	if err != nil {
		return faucetConfig{}, err
	}
	cooldown, err := a.settingService.GetFaucetIpCooldownMinutes()
	if err != nil {
		return faucetConfig{}, err
	}
	ipDaily, err := a.settingService.GetFaucetIpDailyLimit()
	if err != nil {
		return faucetConfig{}, err
	}
	globalDaily, err := a.settingService.GetFaucetGlobalDailyLimit()
	if err != nil {
		return faucetConfig{}, err
	}
	siteKey, err := a.settingService.GetFaucetTurnstileSiteKey()
	if err != nil {
		return faucetConfig{}, err
	}
	secret, err := a.settingService.GetFaucetTurnstileSecret()
	if err != nil {
		return faucetConfig{}, err
	}
	if trafficMB <= 0 {
		trafficMB = 1
	}
	if expireHours <= 0 {
		expireHours = 24
	}
	return faucetConfig{
		Path:              normalizeFaucetPath(path),
		InboundIDs:        parseFaucetInboundIDs(rawInboundIDs),
		ClientFlow:        strings.TrimSpace(clientFlow),
		TrafficBytes:      int64(trafficMB) * 1024 * 1024,
		ExpireHours:       expireHours,
		LimitIP:           limitIP,
		IpCooldownMinutes: cooldown,
		IpDailyLimit:      ipDaily,
		GlobalDailyLimit:  globalDaily,
		TurnstileSiteKey:  strings.TrimSpace(siteKey),
		TurnstileSecret:   strings.TrimSpace(secret),
	}, nil
}

func (a *FaucetController) verifyTurnstile(c *gin.Context, cfg faucetConfig, ip string) error {
	if cfg.TurnstileSiteKey == "" || cfg.TurnstileSecret == "" {
		return nil
	}
	token := strings.TrimSpace(c.PostForm("cf-turnstile-response"))
	if token == "" {
		return common.NewError("turnstile verification is required")
	}
	form := url.Values{}
	form.Set("secret", cfg.TurnstileSecret)
	form.Set("response", token)
	if ip != "" && ip != "unknown" {
		form.Set("remoteip", ip)
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := a.settingService.NewProxiedHTTPClient(10 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if !out.Success {
		return common.NewError("turnstile verification failed")
	}
	return nil
}

func (a *FaucetController) hashText(value string) (string, error) {
	secret, err := a.settingService.GetSecret()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (a *FaucetController) linkHost(c *gin.Context) string {
	_, host, _, _ := a.subService.ResolveRequest(c)
	return host
}

func (a *FaucetController) setClaimCookie(c *gin.Context, cfg faucetConfig, token string, expiresAt int64) {
	maxAge := int(time.Until(time.UnixMilli(expiresAt)).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     faucetCookieName,
		Value:    token,
		Path:     cfg.Path,
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https"),
	})
}

func (a *FaucetController) renderClaimError(c *gin.Context, status int, cfg faucetConfig, err error) {
	a.render(c, status, faucetPageData{
		ClaimAction: cfg.Path + "claim",
		SiteKey:     cfg.TurnstileSiteKey,
		Error:       err.Error(),
	})
}

func (a *FaucetController) render(c *gin.Context, status int, data faucetPageData) {
	if data.ClaimAction == "" {
		data.ClaimAction = normalizeFaucetPath(a.path) + "claim"
	}
	data.Links = buildFaucetShareItems(data.LinksText)
	var body bytes.Buffer
	if err := faucetTemplate.Execute(&body, data); err != nil {
		c.String(http.StatusInternalServerError, "template error")
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(status)
	_, _ = c.Writer.Write(body.Bytes())
}

func (a *FaucetController) remoteIP(c *gin.Context) string {
	remoteIP, ok := extractFaucetIP(c.Request.RemoteAddr)
	if !ok {
		return "unknown"
	}
	if a.isTrustedProxy(remoteIP) {
		for _, header := range []string{"CF-Connecting-IP", "X-Real-IP"} {
			if ip, ok := extractFaucetIP(c.GetHeader(header)); ok {
				return ip
			}
		}
		if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
			for part := range strings.SplitSeq(xff, ",") {
				if ip, ok := extractFaucetIP(part); ok {
					return ip
				}
			}
		}
	}
	return remoteIP
}

func (a *FaucetController) isTrustedProxy(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	trusted, err := a.settingService.GetTrustedProxyCIDRs()
	if err != nil || strings.TrimSpace(trusted) == "" {
		trusted = "127.0.0.1/32,::1/128"
	}
	for value := range strings.SplitSeq(trusted, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			if prefix.Contains(addr) {
				return true
			}
			continue
		}
		if proxyIP, err := netip.ParseAddr(value); err == nil && proxyIP.Unmap() == addr.Unmap() {
			return true
		}
	}
	return false
}

func extractFaucetIP(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return "", false
	}
	return addr.Unmap().String(), true
}

func parseFaucetInboundIDs(raw string) []int {
	var ids []int
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func normalizeFaucetPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/faucet/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path
}

func buildFaucetShareItems(linksText string) []faucetShareItem {
	links := splitLinkLines(linksText)
	items := make([]faucetShareItem, 0, len(links))
	for _, link := range links {
		item := faucetShareItem{Link: link, ConfigText: faucetLinkConfig(link)}
		if png, err := qrcode.Encode(link, qrcode.Medium, 220); err == nil {
			item.QRDataURI = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
		}
		items = append(items, item)
	}
	return items
}

func faucetLinkConfig(raw string) string {
	res, err := linkutil.ParseLink(raw)
	if err != nil || res == nil {
		return "Unable to parse link config."
	}
	b, err := json.MarshalIndent(res.Outbound, "", "  ")
	if err != nil {
		return "Unable to format link config."
	}
	return string(b)
}

var faucetTemplate = template.Must(template.New("faucet").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Traffic Faucet</title>
{{if .SiteKey}}<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>{{end}}
<style>
:root{color-scheme:light dark;font-family:Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f6f7f9;color:#16181d}
body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}
main{width:min(720px,100%);background:#fff;border:1px solid #d9dee7;border-radius:8px;padding:24px;box-shadow:0 10px 32px rgba(20,24,33,.08)}
h1{font-size:24px;line-height:1.2;margin:0 0 8px}
p{margin:0 0 18px;color:#5b6472}
form{display:flex;gap:12px;align-items:center;flex-wrap:wrap}
button{border:0;border-radius:6px;background:#1677ff;color:#fff;font-size:15px;font-weight:600;padding:10px 16px;cursor:pointer}
button:hover{background:#0958d9}
.error{margin:0 0 16px;padding:10px 12px;border-radius:6px;background:#fff1f0;color:#a8071a;border:1px solid #ffccc7}
.result{display:grid;gap:14px}
.node{display:grid;gap:12px;border:1px solid #d9dee7;border-radius:8px;padding:14px}
.node-head{display:flex;gap:16px;align-items:flex-start;flex-wrap:wrap}
.qr{width:180px;height:180px;border:1px solid #d9dee7;border-radius:6px;background:#fff;padding:8px;box-sizing:border-box}
.link-box{flex:1;min-width:240px}
label{display:block;margin:0 0 6px;font-size:13px;font-weight:700;color:#3f4754}
textarea{width:100%;min-height:96px;box-sizing:border-box;border:1px solid #c8d0dc;border-radius:6px;padding:12px;font:13px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;resize:vertical}
pre{margin:0;white-space:pre-wrap;word-break:break-word;border:1px solid #c8d0dc;border-radius:6px;padding:12px;font:13px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;background:#f8fafc}
.meta{font-size:13px;color:#697386}
@media (max-width:520px){body{padding:14px}main{padding:18px}.qr{width:160px;height:160px}.link-box{min-width:0}}
@media (prefers-color-scheme:dark){:root{background:#101216;color:#eef1f6}main{background:#171a21;border-color:#2a303b;box-shadow:none}p,.meta{color:#a9b2c1}.node,.qr{border-color:#2a303b}label{color:#c8d0dc}textarea,pre{background:#101216;color:#eef1f6;border-color:#3a4250}.error{background:#2b1719;color:#ffb3b0;border-color:#5c2227}}
</style>
</head>
<body>
<main>
<h1>Traffic Faucet</h1>
{{if .Error}}<div class="error">{{.Error}}</div>{{end}}
{{if .LinksText}}
<div class="result">
<p>Your temporary proxy links are ready.</p>
{{range .Links}}
<section class="node">
<div class="node-head">
{{if .QRDataURI}}<img class="qr" src="{{.QRDataURI}}" alt="Proxy link QR code">{{end}}
<div class="link-box">
<label>Proxy link</label>
<textarea readonly onclick="this.select()">{{.Link}}</textarea>
</div>
</div>
<div>
<label>Configuration</label>
<pre>{{.ConfigText}}</pre>
</div>
</section>
{{end}}
{{if .ExpiresAt}}<div class="meta">Expires at {{.ExpiresAt}}</div>{{end}}
</div>
{{else}}
<p>Claim a temporary proxy node from this panel.</p>
<form method="post" action="{{.ClaimAction}}">
{{if .SiteKey}}<div class="cf-turnstile" data-sitekey="{{.SiteKey}}"></div>{{end}}
<button type="submit">Claim</button>
</form>
{{end}}
</main>
</body>
</html>`))
