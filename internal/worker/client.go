package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"novel-bot/internal/sessions"
)

const (
	ClientHeader = "X-Novel-Client-ID"
	WorkerHeader = "X-Novel-Worker-Token"
)

// Client only talks to worker origins selected from the trusted directory.
type Client struct {
	token string
	http  *http.Client
}

func NewClient(token string) *Client {
	return &Client{token: token, http: &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext, MaxIdleConns: 100, MaxIdleConnsPerHost: 10, IdleConnTimeout: 30 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) Create(ctx context.Context, r sessions.Record) (sessions.Session, error) {
	body, _ := json.Marshal(struct {
		ID string `json:"id"`
	}{r.ID})
	response, err := c.request(ctx, r, http.MethodPost, "/internal/sessions", body)
	if err != nil {
		return sessions.Session{}, err
	}
	defer response.Body.Close()
	if err := statusError(response); err != nil {
		return sessions.Session{}, err
	}
	var record sessions.Record
	if err := json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&record); err != nil {
		return sessions.Session{}, err
	}
	if record.ID != r.ID || record.ClientID != r.ClientID || record.WorkerToken != r.WorkerToken || record.WorkerURL != r.WorkerURL {
		return sessions.Session{}, errors.New("invalid worker response")
	}
	return record.Session(), nil
}
func (c *Client) Delete(ctx context.Context, r sessions.Record) error {
	response, err := c.request(ctx, r, http.MethodDelete, "/internal/sessions/"+r.ID, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return statusError(response)
}
func (c *Client) request(ctx context.Context, r sessions.Record, method, path string, body []byte) (*http.Response, error) {
	u, err := Origin(r.WorkerURL)
	if err != nil {
		return nil, err
	}
	u.Path = path
	request, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set(ClientHeader, r.ClientID)
	request.Header.Set(WorkerHeader, r.WorkerToken)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(request)
}
func statusError(response *http.Response) error {
	switch response.StatusCode {
	case 201, 204:
		return nil
	case 404:
		return sessions.ErrNotFound
	case 503:
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body); err == nil && body.Error.Code == "session_capacity_reached" {
			return sessions.ErrCapacity
		}
		return errors.New("worker unavailable")
	case 504:
		return context.DeadlineExceeded
	default:
		return errors.New("worker operation failed")
	}
}

// Origin returns a validated origin; used by the WebSocket gateway as well.
func Origin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid worker origin")
	}
	return u, nil
}
