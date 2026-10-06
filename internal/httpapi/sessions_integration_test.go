package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"

	"novel-bot/internal/browser"
)

// Opt-in real Chromium test; production launcher and authenticated HTTP proxy.
func TestChromiumSessionIntegration(t *testing.T) {
	path := os.Getenv("TEST_CHROMIUM_PATH")
	if path == "" {
		t.Skip("set TEST_CHROMIUM_PATH to test actual Chromium launch and CDP")
	}
	profiles := t.TempDir()
	launcher, err := browser.NewChromium(path, profiles, true)
	if err != nil {
		t.Fatal(err)
	}
	server := sessionTestServer(t, launcher, 0)
	id, endpoint := createBrowserSession(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: map[string][]string{"Authorization": {"Bearer owner"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"id":1,"method":"Browser.getVersion"}`)); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID     int `json:"id"`
		Result struct {
			Product string `json:"product"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.ID != 1 || result.Result.Product == "" {
		t.Fatalf("invalid browser CDP response: %s %v", data, err)
	}
	if res := sessionRequest(t, server, "DELETE", "/sessions/"+id, "", "owner"); res.StatusCode != 204 {
		t.Fatal("delete failed")
	}
	entries, err := os.ReadDir(profiles)
	if err != nil || len(entries) != 0 {
		t.Fatalf("profile leaked after browser deletion: %v %v", entries, err)
	}
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("deleted Chromium connection still open")
	}
}
