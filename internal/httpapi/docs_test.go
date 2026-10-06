package httpapi

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

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
	document, err := openapi3.NewLoader().LoadFromData(api.Specification)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatalf("OpenAPI validation: %v", err)
	}
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
		"/sessions": {"post"}, "/sessions/{id}": {"get", "delete"},
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
			case "/v1/whoami", "/sessions", "/sessions/{id}":
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

// Detect newly mounted public route patterns missing from the contract. Method
// coverage is checked above; actual response schemas are exercised by e2e.
func TestOpenAPICoversMountedRoutePatterns(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(api.Specification)
	if err != nil {
		t.Fatal(err)
	}
	patterns := map[string]bool{}
	for _, file := range []string{"router.go", "sessions.go"} {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Handle" && selector.Sel.Name != "HandleFunc") {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			path, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path != "/" && !strings.HasSuffix(path, "/") {
				patterns[path] = true
			}
			return true
		})
	}
	if len(patterns) != document.Paths.Len() {
		t.Fatalf("mounted route count %d differs from OpenAPI %d", len(patterns), document.Paths.Len())
	}
	for path := range patterns {
		if document.Paths.Value(path) == nil {
			t.Fatalf("mounted route %s missing from OpenAPI", path)
		}
	}
}
