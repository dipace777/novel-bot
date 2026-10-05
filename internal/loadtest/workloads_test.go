package loadtest

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestFixturesServeHTTPWorkloadsAndData(t *testing.T) {
	h := FixtureHandler()
	for _, path := range []string{"/article", "/feed", "/dashboard", "/records", "/image.svg"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Header().Get("Content-Type") == "" || w.Body.Len() == 0 {
			t.Fatal("fixture unavailable", path)
		}
		if path == "/records" {
			var data []struct {
				ID          int
				Title, Body string
			}
			if json.Unmarshal(w.Body.Bytes(), &data) != nil || len(data) != 200 || data[0].Title == "" {
				t.Fatal("fixture data missing")
			}
		}
	}
}
