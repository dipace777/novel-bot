package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// The default is a controlled DOM workload; external sites may behave very differently.
func DefaultTarget() string {
	return "data:text/html," + url.PathEscape(`<html><body><script>for(let i=0;i<5000;i++){let d=document.createElement('div');d.textContent='Novel Bot benchmark row '+i;document.body.appendChild(d)}</script></body></html>`)
}

type cdp struct {
	conn  *websocket.Conn
	next  int
	loads map[string]bool
}

func (c *cdp) receive(ctx context.Context) (map[string]json.RawMessage, error) {
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("CDP connection closed")
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return nil, fmt.Errorf("invalid CDP response")
	}
	var method string
	_ = json.Unmarshal(message["method"], &method)
	if method == "Page.lifecycleEvent" {
		var event struct {
			Name     string `json:"name"`
			LoaderID string `json:"loaderId"`
		}
		if json.Unmarshal(message["params"], &event) == nil && event.Name == "load" {
			c.loads[event.LoaderID] = true
		}
	}
	return message, nil
}
func (c *cdp) call(ctx context.Context, method string, params any, session string) (json.RawMessage, error) {
	c.next++
	id := c.next
	request := map[string]any{"id": id, "method": method, "params": params}
	if session != "" {
		request["sessionId"] = session
	}
	data, _ := json.Marshal(request)
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return nil, fmt.Errorf("CDP write failed")
	}
	for {
		message, err := c.receive(ctx)
		if err != nil {
			return nil, err
		}
		var reply int
		_ = json.Unmarshal(message["id"], &reply)
		if reply != id {
			continue
		}
		if len(message["error"]) > 0 {
			return nil, fmt.Errorf("CDP command rejected")
		}
		return message["result"], nil
	}
}
func attach(ctx context.Context, cfg Config, endpoint string, httpClient *http.Client) (*cdp, string, error) {
	expected, _ := url.Parse(cfg.APIURL)
	target, err := url.Parse(endpoint)
	scheme := "ws"
	if expected.Scheme == "https" {
		scheme = "wss"
	}
	if err != nil || target.Scheme != scheme || !strings.EqualFold(target.Host, expected.Host) || target.User != nil || target.RawQuery != "" || target.Fragment != "" || !strings.HasPrefix(target.Path, "/sessions/") {
		return nil, "", fmt.Errorf("CDP URL must match the public API origin")
	}
	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: httpClient, HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.APIKey}}})
	if err != nil {
		return nil, "", fmt.Errorf("CDP connection failed")
	}
	conn.SetReadLimit(1024 * 1024)
	client := &cdp{conn: conn, loads: make(map[string]bool)}
	success := false
	defer func() {
		if !success {
			_ = conn.CloseNow()
		}
	}()
	if _, err := client.call(ctx, "Browser.getVersion", map[string]any{}, ""); err != nil {
		return nil, "", err
	}
	result, err := client.call(ctx, "Target.createTarget", map[string]any{"url": "about:blank"}, "")
	if err != nil {
		return nil, "", err
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if json.Unmarshal(result, &created) != nil || created.TargetID == "" {
		return nil, "", fmt.Errorf("CDP target unavailable")
	}
	result, err = client.call(ctx, "Target.attachToTarget", map[string]any{"targetId": created.TargetID, "flatten": true}, "")
	if err != nil {
		return nil, "", err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(result, &attached) != nil || attached.SessionID == "" {
		return nil, "", fmt.Errorf("CDP page session unavailable")
	}
	if _, err := client.call(ctx, "Page.enable", map[string]any{}, attached.SessionID); err != nil {
		return nil, "", err
	}
	if _, err := client.call(ctx, "Page.setLifecycleEventsEnabled", map[string]any{"enabled": true}, attached.SessionID); err != nil {
		return nil, "", err
	}
	result, err = client.call(ctx, "Page.navigate", map[string]any{"url": cfg.TargetURL}, attached.SessionID)
	if err != nil {
		return nil, "", err
	}
	var navigation struct {
		ErrorText string `json:"errorText"`
		LoaderID  string `json:"loaderId"`
	}
	if json.Unmarshal(result, &navigation) != nil || navigation.ErrorText != "" || navigation.LoaderID == "" {
		return nil, "", fmt.Errorf("page navigation failed")
	}
	for !client.loads[navigation.LoaderID] {
		if _, err := client.receive(ctx); err != nil {
			return nil, "", err
		}
	}
	if err := client.probe(ctx, attached.SessionID); err != nil {
		return nil, "", err
	}
	success = true
	return client, attached.SessionID, nil
}
func (c *cdp) probe(ctx context.Context, session string) error {
	result, err := c.call(ctx, "Runtime.evaluate", map[string]any{"expression": "document.readyState", "returnByValue": true}, session)
	if err != nil {
		return err
	}
	var response struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	if json.Unmarshal(result, &response) != nil || response.Result.Value != "complete" || len(response.Exception) > 0 {
		return fmt.Errorf("page did not finish loading")
	}
	return nil
}
func (c *cdp) hold(ctx context.Context, done <-chan struct{}, timeout time.Duration, session string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			probe, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return c.probe(probe, session)
		case <-ticker.C:
			probe, cancel := context.WithTimeout(ctx, timeout)
			err := c.probe(probe, session)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}
