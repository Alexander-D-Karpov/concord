package swagger

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Alexander-D-Karpov/concord/internal/version"
	"go.uber.org/zap"
)

// swaggerUI holds the embedded Swagger UI static assets served by serveStatic.
//
//go:embed swagger-ui/*
var swaggerUI embed.FS

// swaggerHTMLTemplate is the Swagger UI host page. Its {{.SpecURL}} placeholder
// is filled with the OpenAPI spec URL, and it injects a bearer token from
// localStorage into outgoing requests.
var swaggerHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Concord API Documentation</title>
    <link rel="stylesheet" type="text/css" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
    <style>
        html { box-sizing: border-box; overflow: -moz-scrollbars-vertical; overflow-y: scroll; }
        *, *:before, *:after { box-sizing: inherit; }
        body { margin: 0; background: #fafafa; }
        .swagger-ui .topbar { display: none; }
    </style>
</head>
<body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-standalone-preset.js"></script>
    <script>
        window.onload = function() {
            window.ui = SwaggerUIBundle({
                url: "{{.SpecURL}}",
                dom_id: '#swagger-ui',
                deepLinking: true,
                presets: [
                    SwaggerUIBundle.presets.apis,
                    SwaggerUIStandalonePreset
                ],
                plugins: [
                    SwaggerUIBundle.plugins.DownloadUrl
                ],
                layout: "StandaloneLayout",
                persistAuthorization: true,
                requestInterceptor: function(req) {
                    var token = window.localStorage.getItem('swagger_auth_token');
                    if (token && !req.headers['Authorization']) {
                        req.headers['Authorization'] = 'Bearer ' + token;
                    }
                    return req;
                }
            });
        }
    </script>
</body>
</html>`

// Handler serves the Swagger UI and the OpenAPI spec (augmented at load time
// with bearer-auth definitions and version info) under basePath.
type Handler struct {
	logger   *zap.Logger
	specPath string
	basePath string
	spec     []byte
	htmlTmpl *template.Template
}

// NewHandler parses the UI template and loads/augments the spec from specPath,
// serving everything under basePath (trailing slash trimmed). It returns an
// error if the template fails to parse or the spec cannot be read or decoded.
func NewHandler(specPath, basePath string, logger *zap.Logger) (*Handler, error) {
	h := &Handler{
		logger:   logger,
		specPath: specPath,
		basePath: strings.TrimSuffix(basePath, "/"),
	}

	tmpl, err := template.New("swagger").Parse(swaggerHTMLTemplate)
	if err != nil {
		return nil, err
	}
	h.htmlTmpl = tmpl

	if err := h.loadSpec(); err != nil {
		return nil, err
	}

	return h, nil
}

// loadSpec reads the OpenAPI file, injects a Bearer security scheme and makes it
// the global requirement, overrides the info block with the Concord title and
// version, and caches the re-marshaled spec in h.spec.
func (h *Handler) loadSpec() error {
	data, err := os.ReadFile(h.specPath)
	if err != nil {
		return err
	}

	var spec map[string]interface{}
	if err := json.Unmarshal(data, &spec); err != nil {
		return err
	}

	spec["securityDefinitions"] = map[string]interface{}{
		"Bearer": map[string]interface{}{
			"type":        "apiKey",
			"name":        "Authorization",
			"in":          "header",
			"description": "Enter 'Bearer {token}' to authorize",
		},
	}

	spec["security"] = []map[string]interface{}{
		{"Bearer": []string{}},
	}

	spec["info"] = map[string]interface{}{
		"title":       "Concord API",
		"description": fmt.Sprintf("Voice chat and messaging platform API\n\nCodename: %s", version.APICodename()),
		"version":     version.API(),
	}

	h.spec, err = json.MarshalIndent(spec, "", "  ")
	return err
}

// ServeHTTP routes requests under basePath: the root and index.html serve the
// UI page, spec.json/openapi.json serve the spec, and anything else is served
// from the embedded static assets.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, h.basePath)
	path = strings.TrimPrefix(path, "/")

	switch path {
	case "", "index.html":
		h.serveUI(w, r)
	case "spec.json", "openapi.json":
		h.serveSpec(w, r)
	default:
		h.serveStatic(w, r, path)
	}
}

// serveUI renders the Swagger UI HTML page with the spec URL derived from
// basePath, responding 500 if template execution fails.
func (h *Handler) serveUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := struct {
		SpecURL string
	}{
		SpecURL: h.basePath + "/spec.json",
	}
	if err := h.htmlTmpl.Execute(w, data); err != nil {
		h.logger.Error("failed to render swagger UI", zap.Error(err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// serveSpec writes the cached, augmented OpenAPI spec as JSON with a permissive
// CORS header so external Swagger UIs can load it.
func (h *Handler) serveSpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, err := w.Write(h.spec)
	if err != nil {
		return
	}
}

// serveStatic serves a file from the embedded swagger-ui assets, responding 404
// if the embedded subtree cannot be opened.
func (h *Handler) serveStatic(w http.ResponseWriter, r *http.Request, path string) {
	subFS, err := fs.Sub(swaggerUI, "swagger-ui")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.FileServer(http.FS(subFS)).ServeHTTP(w, r)
}

// GenerateSpec is currently a stub: it only ensures the output directory exists
// and returns nil without generating any spec. Spec generation is handled
// out-of-band by the proto tooling.
func GenerateSpec(protoDir, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}
	return nil
}
