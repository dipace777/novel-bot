package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"novel-bot/api"
)

func TestDocumentationIsPublicAndEmbedded(t *testing.T) {
	router := accountRouter(&keySpy{}, &accountStub{})
	for _, tc := range []struct{ path, contentType, contains string }{
		{"/docs/", "text/html", "Novel Bot API"},
		{"/docs/swagger-ui-bundle.js", "javascript", "SwaggerUIBundle"},
		{"/docs/swagger-ui.css", "text/css", ".swagger-ui"},
		{"/docs/theme.css", "text/css", ".quickstart"},
		{"/docs/swagger-initializer.js", "javascript", "persistAuthorization: false"},
		{"/openapi.json", "application/json", `"openapi": "3.0.3"`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			response := apiRequest(router, "GET", tc.path, "", "")
			if response.Code != 200 || !strings.Contains(response.Header().Get("Content-Type"), tc.contentType) || !strings.Contains(response.Body.String(), tc.contains) {
				t.Fatalf("documentation response: status %d, content-type %q", response.Code, response.Header().Get("Content-Type"))
			}
		})
	}
	response := apiRequest(router, "GET", "/docs", "", "")
	if response.Code != 307 || response.Header().Get("Location") != "/docs/" {
		t.Fatal("docs redirect missing")
	}
	for _, path := range []string{"/docs/", "/openapi.json"} {
		response := apiRequest(router, "POST", path, "", "")
		if response.Code != 405 {
			t.Fatalf("unsupported documentation method accepted: %s", path)
		}
	}
	for _, path := range []string{"/docs/unknown", "/docs/README.md", "/docs/LICENSE"} {
		response := apiRequest(router, "GET", path, "", "")
		if response.Code != 404 {
			t.Fatalf("unexpected public asset: %s", path)
		}
	}
	// HEAD must be accepted without requiring API credentials.
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("HEAD", "/openapi.json", nil))
	if response.Code != 200 {
		t.Fatal("HEAD specification request rejected")
	}
}

func TestOpenAPIContractAndLocalReferences(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(api.Specification, &spec); err != nil {
		t.Fatal(err)
	}
	if spec["openapi"] != "3.0.3" {
		t.Fatal("unexpected OpenAPI version")
	}
	paths := spec["paths"].(map[string]any)
	expected := map[string][]string{
		"/v1/auth/register": {"post"}, "/v1/auth/login": {"post"},
		"/v1/auth/me": {"get"}, "/v1/auth/logout": {"post"},
		"/v1/api-keys": {"get", "post"}, "/v1/api-keys/{id}": {"delete"},
		"/v1/whoami": {"get"}, "/healthz": {"get"}, "/readyz": {"get"},
	}
	if len(paths) != len(expected) {
		t.Fatalf("unexpected documented route count: %d", len(paths))
	}
	operationIDs := map[string]bool{}
	for path, methods := range expected {
		pathItem, ok := paths[path].(map[string]any)
		if !ok || len(pathItem) != len(methods) {
			t.Fatalf("missing or extra operations for %s", path)
		}
		for _, method := range methods {
			op, ok := pathItem[method].(map[string]any)
			if !ok {
				t.Fatalf("missing %s %s", method, path)
			}
			id, _ := op["operationId"].(string)
			if id == "" || operationIDs[id] {
				t.Fatalf("missing/duplicate operation ID: %q", id)
			}
			operationIDs[id] = true
			security := op["security"].([]any)
			switch path {
			case "/v1/auth/register", "/v1/auth/login", "/healthz", "/readyz":
				if len(security) != 0 {
					t.Fatalf("public route requires credentials: %s", path)
				}
			case "/v1/whoami":
				if len(security) != 2 || security[0].(map[string]any)["ApplicationBearer"] == nil || security[1].(map[string]any)["ApplicationKeyHeader"] == nil {
					t.Fatal("application credential alternatives missing")
				}
			default:
				if len(security) != 1 || security[0].(map[string]any)["LoginSession"] == nil {
					t.Fatalf("account route accepts incorrect credential: %s", path)
				}
			}
			responses := op["responses"].(map[string]any)
			if len(responses) == 0 {
				t.Fatalf("responses missing: %s", path)
			}
		}
	}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				if !strings.HasPrefix(ref, "#/") {
					t.Fatalf("external reference: %s", ref)
				}
				var target any = spec
				for _, segment := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
					object, ok := target.(map[string]any)
					if !ok {
						t.Fatalf("invalid reference: %s", ref)
					}
					target, ok = object[segment]
					if !ok {
						t.Fatalf("unresolved reference: %s", ref)
					}
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(spec)
	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	for _, name := range []string{"User", "APIKey"} {
		properties := schemas[name].(map[string]any)["properties"].(map[string]any)
		for _, secret := range []string{"password", "password_hash", "digest", "api_key"} {
			if _, ok := properties[secret]; ok {
				t.Fatalf("secret documented in metadata: %s.%s", name, secret)
			}
		}
	}
}
