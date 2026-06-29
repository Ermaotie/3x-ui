package service

import (
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestCreateFaucetClientPreservesEmptySubID(t *testing.T) {
	setupBulkDB(t)

	svc := &ClientService{}
	inboundSvc := &InboundService{}
	ib := mkInboundStream(t, 21001, model.VLESS, `{"clients":[],"decryption":"none"}`, `{"network":"tcp","security":"tls"}`)

	_, err := svc.CreateFaucet(inboundSvc, &ClientCreatePayload{
		Client: model.Client{
			Email:      "faucet-client@x",
			ID:         "11111111-1111-4111-8111-111111111111",
			Enable:     true,
			Flow:       "xtls-rprx-vision",
			TotalGB:    1024,
			ExpiryTime: 1893456000000,
		},
		InboundIds: []int{ib.Id},
	})
	if err != nil {
		t.Fatalf("CreateFaucet: %v", err)
	}

	var rec model.ClientRecord
	if err := database.GetDB().Where("email = ?", "faucet-client@x").First(&rec).Error; err != nil {
		t.Fatalf("load client record: %v", err)
	}
	if rec.SubID != "" {
		t.Fatalf("record subId = %q, want empty", rec.SubID)
	}
	if rec.Flow != "xtls-rprx-vision" {
		t.Fatalf("record flow = %q, want xtls-rprx-vision", rec.Flow)
	}

	reloaded, err := inboundSvc.GetInbound(ib.Id)
	if err != nil {
		t.Fatalf("GetInbound: %v", err)
	}
	var settings struct {
		Clients []map[string]any `json:"clients"`
	}
	if err := json.Unmarshal([]byte(reloaded.Settings), &settings); err != nil {
		t.Fatalf("unmarshal inbound settings: %v", err)
	}
	if len(settings.Clients) != 1 {
		t.Fatalf("settings clients len = %d, want 1", len(settings.Clients))
	}
	if got, _ := settings.Clients[0]["subId"].(string); got != "" {
		t.Fatalf("settings subId = %q, want empty", got)
	}
	if got, _ := settings.Clients[0]["flow"].(string); got != "xtls-rprx-vision" {
		t.Fatalf("settings flow = %q, want xtls-rprx-vision", got)
	}
}
