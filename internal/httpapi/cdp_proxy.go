package httpapi

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

var cdpTransport = &http.Transport{
	// Do not honor environment proxies for worker-local debugging endpoints.
	DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ResponseHeaderTimeout: 5 * time.Second,
	MaxIdleConns:          100,
	IdleConnTimeout:       30 * time.Second,
}

func (h sessionHandlers) connect(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	s, err := h.manager.Get(r.Context(), principal.ClientID, r.PathValue("id"))
	if err != nil {
		h.sessionError(w, r, err)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeError(w, r, http.StatusBadRequest, "websocket_required", "A WebSocket upgrade is required")
		return
	}
	target, err := url.Parse(s.Endpoint)
	if err != nil || target.Scheme != "ws" || target.Hostname() != "127.0.0.1" || target.Port() == "" || !strings.HasPrefix(target.Path, "/devtools/browser/") {
		writeError(w, r, http.StatusServiceUnavailable, "browser_unavailable", "Browser endpoint unavailable")
		return
	}
	target.Scheme = "http"
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-s.Done:
			cancel()
		case <-ctx.Done():
		}
	}()
	proxy := &httputil.ReverseProxy{
		Transport: cdpTransport,
		Rewrite: func(p *httputil.ProxyRequest) {
			p.Out.URL = &url.URL{Scheme: target.Scheme, Host: target.Host, Path: target.Path}
			p.Out.Host = target.Host
			// Authentication is terminated at the API, never forwarded to Chromium.
			for _, name := range []string{"Authorization", "X-API-Key", "Cookie", "Origin", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
				p.Out.Header.Del(name)
			}
		},
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			h.logger.ErrorContext(r.Context(), "CDP connection failed", "request_id", r.Context().Value(requestIDKey))
			writeError(w, r, http.StatusBadGateway, "cdp_unavailable", "Browser connection unavailable")
		},
	}
	// REST deadlines must not terminate a long-lived WebSocket after upgrading.
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Time{}); err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Unable to establish browser connection")
		return
	}
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Unable to establish browser connection")
		return
	}
	proxy.ServeHTTP(w, r.WithContext(ctx))
}
