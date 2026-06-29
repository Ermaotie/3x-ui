package sub

import (
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestFaucetRenderShowsQRAndConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/faucet/", nil)

	link := "vless://11111111-2222-4333-8444-555555555555@example.com:8443?type=tcp&security=reality&flow=xtls-rprx-vision&pbk=PBK&sid=SID&sni=www.cloudflare.com&fp=chrome#test-node"
	(&FaucetController{path: "/faucet/"}).render(c, http.StatusOK, faucetPageData{LinksText: link})

	got := html.UnescapeString(w.Body.String())
	for _, want := range []string{
		"data:image/png;base64,",
		`"address": "example.com"`,
		`"port": 8443`,
		`"flow": "xtls-rprx-vision"`,
		`"security": "reality"`,
		`"publicKey": "PBK"`,
		`"shortId": "SID"`,
		`"serverName": "www.cloudflare.com"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered faucet page missing %q:\n%s", want, got)
		}
	}
}

func TestFaucetLoadConfigUsesTrafficMegabytes(t *testing.T) {
	initSubDB(t)
	if err := database.GetDB().Create(&model.Setting{Key: "faucetTrafficMB", Value: "512"}).Error; err != nil {
		t.Fatalf("seed faucetTrafficMB: %v", err)
	}

	cfg, err := (&FaucetController{}).loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	want := int64(512 * 1024 * 1024)
	if cfg.TrafficBytes != want {
		t.Fatalf("TrafficBytes = %d, want %d", cfg.TrafficBytes, want)
	}
}

func TestFaucetLoadConfigFallsBackToLegacyTrafficGigabytes(t *testing.T) {
	initSubDB(t)
	if err := database.GetDB().Create(&model.Setting{Key: "faucetTrafficGB", Value: "2"}).Error; err != nil {
		t.Fatalf("seed faucetTrafficGB: %v", err)
	}

	cfg, err := (&FaucetController{}).loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	want := int64(2 * 1024 * 1024 * 1024)
	if cfg.TrafficBytes != want {
		t.Fatalf("TrafficBytes = %d, want %d", cfg.TrafficBytes, want)
	}
}
