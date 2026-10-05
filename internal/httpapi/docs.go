package httpapi

import (
	"net/http"
	"strings"

	"novel-bot/api"
)

func mountDocumentation(mux *http.ServeMux) {
	mux.Handle("/openapi.json", getOnly(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(api.Specification)
	}))
	mux.Handle("/docs", getOnly(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusTemporaryRedirect)
	}))
	mux.Handle("/docs/", getOnly(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/docs/")
		if name == "" {
			name = "index.html"
		}
		switch name {
		case "index.html", "swagger-ui-bundle.js", "swagger-ui.css", "theme.css", "swagger-initializer.js":
			// Serve only the UI's public assets, without filesystem listings.
			http.ServeFileFS(w, r, api.SwaggerUI, "swagger-ui/"+name)
		default:
			writeError(w, r, http.StatusNotFound, "not_found", "Documentation asset not found")
		}
	}))
}
