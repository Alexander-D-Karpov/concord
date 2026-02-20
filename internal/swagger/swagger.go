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

//go:embed swagger-ui/*
var swaggerUI embed.FS

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

type Handler struct {
	logger   *zap.Logger
	specPath string
	basePath string
	spec     []byte
	htmlTmpl *template.Template
}

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

func (h *Handler) serveSpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, err := w.Write(h.spec)
	if err != nil {
		return
	}
}

func (h *Handler) serveStatic(w http.ResponseWriter, r *http.Request, path string) {
	subFS, err := fs.Sub(swaggerUI, "swagger-ui")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.FileServer(http.FS(subFS)).ServeHTTP(w, r)
}

func GenerateSpec(protoDir, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}
	return nil
}
