package platform

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func jsonResponse(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func bad(w http.ResponseWriter, err error) {
	jsonResponse(w, 409, map[string]string{"error": err.Error()})
}
func readJSON(w http.ResponseWriter, r *http.Request, v any, strict bool) error {
	defer r.Body.Close()
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if strict {
		d.DisallowUnknownFields()
	}
	if d.Decode(v) != nil {
		return errors.New("请求 JSON 无效或超过大小限制")
	}
	var rest any
	if d.Decode(&rest) != io.EOF {
		return errors.New("请求包含多余内容")
	}
	return nil
}
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}

type alertPayload struct {
	Version string `json:"version"`
	Status  string `json:"status"`
	Alerts  []struct {
		Status      string            `json:"status"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		StartsAt    time.Time         `json:"startsAt"`
		Fingerprint string            `json:"fingerprint"`
	} `json:"alerts"`
}

func (s *Service) Ingest(payload alertPayload) error {
	if payload.Version != "4" || len(payload.Alerts) == 0 || len(payload.Alerts) > 100 {
		return errors.New("需要 Alertmanager v4 Webhook，单次 1–100 条告警")
	}
	return s.change(func(st *DiskState) error {
		if st.Config.PublicURL == "" {
			return errors.New("请先完成告警接入配置")
		}
		for _, a := range payload.Alerts {
			if a.Status != "firing" && a.Status != "resolved" {
				return errors.New("告警状态必须为 firing 或 resolved")
			}
			if len(a.Labels) > 100 || len(a.Annotations) > 100 {
				return errors.New("告警字段过多")
			}
			fingerprint := a.Fingerprint
			if fingerprint == "" {
				b, _ := json.Marshal(a.Labels)
				sum := sha256.Sum256(b)
				fingerprint = hex.EncodeToString(sum[:])
			}
			if len(fingerprint) > 128 {
				return errors.New("告警指纹过长")
			}
			name := a.Labels["alertname"]
			if name == "" || len(name) > 256 {
				return errors.New("缺少有效 alertname")
			}
			summary := a.Annotations["summary"]
			if len(summary) > 4000 {
				summary = summary[:4000]
			}
			matched := false
			for i := range st.Alerts {
				old := &st.Alerts[i]
				if old.Fingerprint == fingerprint && old.StartsAt.Equal(a.StartsAt) {
					old.Status = a.Status
					old.ReceivedAt = time.Now().UTC()
					old.Count++
					matched = true
					break
				}
			}
			if !matched {
				st.Alerts = append(st.Alerts, Alert{ID: randomID()[:16], Fingerprint: fingerprint, Name: name, Namespace: a.Labels["namespace"], Summary: summary, Status: a.Status, StartsAt: a.StartsAt, ReceivedAt: time.Now().UTC(), Count: 1})
			}
		}
		if len(st.Alerts) > 200 {
			st.Alerts = st.Alerts[len(st.Alerts)-200:]
		}
		event(st, "webhook.received", "收到 Alertmanager 通知；告警恢复不直接作为业务恢复证明")
		return nil
	})
}
func (s *Service) routes(webDir string) http.Handler {
	mux := http.NewServeMux()
	s.library.routes(mux)
	mux.HandleFunc("GET /api/platform/ping", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]string{"product": "kubepilot-platform", "version": Version})
	})
	mux.HandleFunc("GET /api/platform/state", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, s.View()) })
	mux.HandleFunc("POST /api/platform/setup/source", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			PublicURL string `json:"publicUrl"`
			AllowHTTP bool   `json:"allowHttp"`
		}
		if err := readJSON(w, r, &b, true); err != nil {
			bad(w, err)
			return
		}
		snippet, err := s.SaveSource(b.PublicURL, b.AllowHTTP)
		if err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 200, map[string]string{"config": snippet, "url": strings.TrimRight(b.PublicURL, "/") + "/ingest/alertmanager"})
	})
	mux.HandleFunc("POST /api/platform/setup/source-config", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]string{"config": s.SourceSnippet()})
	})
	mux.HandleFunc("POST /api/platform/setup/model", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			BaseURL string `json:"baseUrl"`
			Model   string `json:"model"`
			APIKey  string `json:"apiKey"`
		}
		if err := readJSON(w, r, &b, true); err != nil {
			bad(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
		defer cancel()
		if err := s.SaveModel(ctx, b.BaseURL, b.Model, b.APIKey); err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/platform/setup/connector", func(w http.ResponseWriter, r *http.Request) {
		var scope Scope
		if err := readJSON(w, r, &scope, true); err != nil {
			bad(w, err)
			return
		}
		manifest, err := s.SaveScope(scope)
		if err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 200, map[string]string{"manifest": manifest})
	})
	mux.HandleFunc("POST /api/platform/setup/manifest", func(w http.ResponseWriter, r *http.Request) {
		manifest, err := s.Manifest()
		if err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 200, map[string]string{"manifest": manifest})
	})
	mux.HandleFunc("POST /api/platform/scan", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Scan(); err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 202, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/platform/pause", func(w http.ResponseWriter, r *http.Request) {
		err := s.change(func(st *DiskState) error {
			st.Config.Scope.Automatic = false
			event(st, "automation.paused", "暂停后不再派发新的自动修复，已提交操作仍需核对恢复结果")
			return nil
		})
		if err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/platform/cases/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Retry(r.PathValue("id")); err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 202, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/platform/cases/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			PlanHash string `json:"planHash"`
		}
		if err := readJSON(w, r, &b, true); err != nil {
			bad(w, err)
			return
		}
		if err := s.Approve(r.PathValue("id"), b.PlanHash); err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 202, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /ingest/alertmanager", func(w http.ResponseWriter, r *http.Request) {
		var p alertPayload
		if err := readJSON(w, r, &p, false); err != nil {
			bad(w, err)
			return
		}
		if err := s.Ingest(p); err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 202, map[string]bool{"accepted": true})
		if !s.busy() {
			_ = s.Scan()
		}
	})
	mux.HandleFunc("POST /connector/poll", func(w http.ResponseWriter, r *http.Request) {
		var p Poll
		if err := readJSON(w, r, &p, true); err != nil {
			bad(w, err)
			return
		}
		reply, err := s.Poll(p)
		if err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 200, reply)
	})
	mux.HandleFunc("POST /connector/result", func(w http.ResponseWriter, r *http.Request) {
		var p JobResult
		if err := readJSON(w, r, &p, true); err != nil {
			bad(w, err)
			return
		}
		if err := s.Result(p); err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { serveWeb(webDir, w, r) })
	return mux
}

func serveWeb(webDir string, w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/connector/") || strings.HasPrefix(r.URL.Path, "/ingest/") {
		jsonResponse(w, 404, map[string]string{"error": "接口不存在"})
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	path := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/")))
	if filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(os.PathSeparator)) || strings.Contains(path, ":") {
		http.NotFound(w, r)
		return
	}
	path = filepath.Join(webDir, path)
	if f, err := os.Stat(path); err != nil || f.IsDir() {
		path = filepath.Join(webDir, "index.html")
	}
	http.ServeFile(w, r, path)
}

// Handler is retained for the isolated service tests. The B/S server uses Hub.Handler.
func (s *Service) Handler(webDir, adminToken string) http.Handler {
	mux := s.routes(webDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if adminToken == "" {
			host := r.Host
			if h, _, e := net.SplitHostPort(host); e == nil {
				host = h
			}
			ip := net.ParseIP(host)
			if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				jsonResponse(w, 403, map[string]string{"error": "外部访问需要平台管理员令牌"})
				return
			}
		}
		switch {
		case r.URL.Path == "/api/platform/ping":
		case strings.HasPrefix(r.URL.Path, "/connector/"):
			if !s.authenticated(bearer(r), true) {
				jsonResponse(w, 401, map[string]string{"error": "Connector 凭证无效"})
				return
			}
		case strings.HasPrefix(r.URL.Path, "/ingest/"):
			if !s.authenticated(bearer(r), false) {
				jsonResponse(w, 401, map[string]string{"error": "Webhook 凭证无效"})
				return
			}
		case strings.HasPrefix(r.URL.Path, "/api/"):
			if adminToken != "" && subtle.ConstantTimeCompare([]byte(bearer(r)), []byte(adminToken)) != 1 {
				jsonResponse(w, 401, map[string]string{"error": "请输入平台访问令牌"})
				return
			}
			if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-Requested-With") != "KubePilot" {
				jsonResponse(w, 403, map[string]string{"error": "写操作需要应用请求标识"})
				return
			}
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" {
				u, e := url.Parse(origin)
				if e != nil || u.Host != r.Host {
					jsonResponse(w, 403, map[string]string{"error": "不允许跨站写入"})
					return
				}
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				jsonResponse(w, 415, map[string]string{"error": "需要 JSON 请求"})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
