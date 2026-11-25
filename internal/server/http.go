package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kubeagent/internal/core"
)

type API struct {
	Engine          *core.Engine
	Agent           *core.AgentRunner
	WebDir          string
	Token           string
	CaptureBaseline func(context.Context, string, string) error
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func failure(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	if errors.Is(err, core.ErrNotFound) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	defer r.Body.Close()
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("请求内容无效")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("请求包含多余内容")
	}
	return nil
}
func (a API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "version": "0.1.0"})
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, a.Engine.State()) })
	mux.HandleFunc("GET /api/runbooks", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, core.Runbooks) })
	a.agentRoutes(mux)
	mutation := func(fn func(context.Context, *http.Request) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
			defer cancel()
			if err := fn(ctx, r); err != nil {
				if auditErr := a.Engine.RecordRejected(r.URL.Path, err); auditErr != nil {
					slog.Warn("could not persist rejected operation", "error", auditErr)
				}
				failure(w, err)
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "revision": a.Engine.State().Revision})
		}
	}
	mux.HandleFunc("POST /api/inspections", mutation(func(ctx context.Context, _ *http.Request) error { return a.Engine.Inspect(ctx, "operator") }))
	mux.HandleFunc("POST /api/incidents/{id}/diagnose", mutation(func(ctx context.Context, r *http.Request) error { return a.Engine.Diagnose(ctx, r.PathValue("id")) }))
	mux.HandleFunc("POST /api/incidents/{id}/execute", mutation(func(ctx context.Context, r *http.Request) error {
		return a.Engine.Execute(ctx, r.PathValue("id"), false)
	}))
	mux.HandleFunc("POST /api/incidents/{id}/verify", mutation(func(ctx context.Context, r *http.Request) error { return a.Engine.Verify(ctx, r.PathValue("id")) }))
	mux.HandleFunc("POST /api/policies/{action}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled   bool `json:"enabled"`
			Automatic bool `json:"automatic"`
		}
		if err := decode(w, r, &body); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := a.Engine.SetPolicy(r.PathValue("action"), body.Enabled, body.Automatic); err != nil {
			failure(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/demo/inject", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Fault string `json:"fault"`
		}
		if err := decode(w, r, &body); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := a.Engine.Inject(body.Fault); err != nil {
			failure(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/demo/reset", mutation(func(ctx context.Context, r *http.Request) error { return a.Engine.ResetDemo() }))
	mux.HandleFunc("POST /api/baselines", func(w http.ResponseWriter, r *http.Request) {
		if a.CaptureBaseline == nil {
			failure(w, errors.New("当前环境不支持采集真实基线"))
			return
		}
		var body struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		}
		if err := decode(w, r, &body); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := a.CaptureBaseline(ctx, body.Kind, body.Name); err != nil {
			failure(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"error": "接口不存在或请求方法不正确"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/")))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) || strings.Contains(clean, ":") {
			http.NotFound(w, r)
			return
		}
		file := filepath.Join(a.WebDir, clean)
		if info, err := os.Stat(file); err == nil && !info.IsDir() {
			http.ServeFile(w, r, file)
			return
		}
		index := filepath.Join(a.WebDir, "index.html")
		if _, err := os.Stat(index); err != nil {
			writeJSON(w, 503, map[string]string{"error": "前端尚未构建，请在 web 目录执行 npm run build"})
			return
		}
		http.ServeFile(w, r, index)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("request panic", "path", r.URL.Path, "error", v)
				writeJSON(w, 500, map[string]string{"error": "服务内部错误"})
			}
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if a.Token == "" {
			host := r.Host
			if parsed, _, err := net.SplitHostPort(host); err == nil {
				host = parsed
			}
			ip := net.ParseIP(host)
			if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				writeJSON(w, 403, map[string]string{"error": "无令牌开发模式仅接受本机 Host"})
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			if a.Token != "" && (!strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), []byte(a.Token)) != 1) {
				writeJSON(w, 401, map[string]string{"error": "需要有效的访问令牌"})
				return
			}
			if r.Method != "GET" && r.Method != "HEAD" {
				origin := r.Header.Get("Origin")
				if origin != "" {
					parsed, err := url.Parse(origin)
					if err != nil || parsed.Host != r.Host {
						writeJSON(w, 403, map[string]string{"error": "不允许跨站写入请求"})
						return
					}
				}
				if r.Header.Get("X-Requested-With") != "KubePilot" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
					writeJSON(w, 403, map[string]string{"error": "写操作需要应用请求标识和 JSON 内容类型"})
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
