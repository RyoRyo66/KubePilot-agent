package platform

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type identityContextKey struct{}
type requestIdentity struct {
	Account   Account
	Workspace *Workspace
	Token     string
}

func principal(r *http.Request) requestIdentity {
	return r.Context().Value(identityContextKey{}).(requestIdentity)
}
func (h *Hub) cookieName(r *http.Request) string {
	if h.secureCookie(r) {
		return "__Host-kubepilot_session"
	}
	return "kubepilot_session"
}
func (h *Hub) secureCookie(r *http.Request) bool {
	return r.TLS != nil || strings.HasPrefix(h.options.PublicOrigin, "https://")
}
func (h *Hub) token(r *http.Request) string {
	c, err := r.Cookie(h.cookieName(r))
	if err != nil {
		return ""
	}
	return c.Value
}
func (h *Hub) setCookie(w http.ResponseWriter, r *http.Request, token string) {
	c := &http.Cookie{Name: h.cookieName(r), Value: token, Path: "/", HttpOnly: true, Secure: h.secureCookie(r), SameSite: http.SameSiteStrictMode, MaxAge: int(sessionLifetime.Seconds()), Expires: time.Now().Add(sessionLifetime)}
	if token == "" {
		c.MaxAge = -1
		c.Expires = time.Unix(1, 0)
	}
	http.SetCookie(w, c)
}
func (h *Hub) sessionView(token string) map[string]any {
	a, ws, ok := h.identity(token)
	if !ok {
		return map[string]any{"authenticated": false, "registrationOpen": !h.options.RegistrationClosed}
	}
	return map[string]any{"authenticated": true, "registrationOpen": !h.options.RegistrationClosed, "account": map[string]any{"id": a.ID, "username": a.Username}, "workspace": ws}
}
func (h *Hub) rate(key string, limit int, duration time.Duration) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for k, b := range h.rates {
		if now.After(b.Until) {
			delete(h.rates, k)
		}
	}
	b := h.rates[key]
	if b.Count == 0 {
		if len(h.rates) >= 4096 {
			return false
		}
		b.Until = now.Add(duration)
	}
	if b.Count >= limit {
		return false
	}
	b.Count++
	h.rates[key] = b
	return true
}
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func (h *Hub) credentialAction(w http.ResponseWriter, r *http.Request, action func()) {
	select {
	case h.passwordSlots <- struct{}{}:
		defer func() { <-h.passwordSlots }()
		action()
	default:
		jsonResponse(w, 429, map[string]string{"error": "登录服务繁忙，请稍后重试"})
	}
}
func (h *Hub) Handler(webDir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/platform/ping", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]string{"product": "kubepilot-platform", "version": Version})
	})
	mux.HandleFunc("GET /api/auth/session", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, h.sessionView(h.token(r))) })
	for _, kind := range []string{"register", "login"} {
		mux.HandleFunc("POST /api/auth/"+kind, func(w http.ResponseWriter, r *http.Request) {
			if kind == "register" && h.options.RegistrationClosed {
				jsonResponse(w, 403, map[string]string{"error": "当前部署已关闭新账号注册"})
				return
			}
			var b struct {
				Username     string `json:"username"`
				Password     string `json:"password"`
				Confirmation string `json:"confirmation"`
			}
			if err := readJSON(w, r, &b, true); err != nil {
				bad(w, err)
				return
			}
			ip := remoteIP(r)
			limited := !h.rate("ip:"+ip, 100, 15*time.Minute)
			if kind == "register" {
				limited = limited || !h.rate("register:"+ip, 5, time.Hour)
			} else {
				limited = limited || !h.rate("login:"+ip+":"+sessionHash(normalizeUsername(b.Username)), 10, 15*time.Minute)
			}
			if limited {
				w.Header().Set("Retry-After", "900")
				jsonResponse(w, 429, map[string]string{"error": "尝试次数过多，请稍后再试"})
				return
			}
			h.credentialAction(w, r, func() {
				var token string
				var err error
				if kind == "register" {
					token, err = h.register(b.Username, b.Password, b.Confirmation)
				} else {
					token, err = h.login(b.Username, b.Password)
				}
				if err != nil {
					code := 409
					if kind == "login" {
						code = 401
					}
					jsonResponse(w, code, map[string]string{"error": err.Error()})
					return
				}
				h.setCookie(w, r, token)
				jsonResponse(w, 200, h.sessionView(token))
			})
		})
	}
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		next := clone(h.state)
		delete(next.Sessions, sessionHash(principal(r).Token))
		err := h.saveLocked(next)
		h.mu.Unlock()
		if err != nil {
			bad(w, err)
			return
		}
		h.setCookie(w, r, "")
		jsonResponse(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/auth/password", func(w http.ResponseWriter, r *http.Request) {
		if !h.rate("password:"+principal(r).Account.ID, 10, 15*time.Minute) {
			jsonResponse(w, 429, map[string]string{"error": "尝试次数过多，请稍后再试"})
			return
		}
		var b struct {
			Current      string `json:"currentPassword"`
			Password     string `json:"password"`
			Confirmation string `json:"confirmation"`
		}
		if err := readJSON(w, r, &b, true); err != nil {
			bad(w, err)
			return
		}
		h.credentialAction(w, r, func() {
			if err := h.changePassword(principal(r).Account.ID, b.Current, b.Password, b.Confirmation); err != nil {
				bad(w, err)
				return
			}
			h.setCookie(w, r, "")
			jsonResponse(w, 200, map[string]bool{"ok": true})
		})
	})
	for _, action := range []string{"create", "update"} {
		mux.HandleFunc("POST /api/workspace/"+action, func(w http.ResponseWriter, r *http.Request) {
			var b struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if err := readJSON(w, r, &b, true); err != nil {
				bad(w, err)
				return
			}
			var ws *Workspace
			var err error
			if action == "create" {
				ws, err = h.createWorkspace(principal(r).Account.ID, b.Name, b.Description)
			} else {
				ws, err = h.updateWorkspace(principal(r).Account.ID, b.Name, b.Description)
			}
			if err != nil {
				bad(w, err)
				return
			}
			jsonResponse(w, 200, map[string]any{"workspace": ws})
		})
	}
	mux.HandleFunc("POST /api/workspace/complete", func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		if p.Workspace == nil {
			bad(w, errors.New("请先创建工作空间"))
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		ws := h.state.Workspaces[p.Workspace.ID]
		s := h.services[ws.ID]
		if s.View()["ready"] != true {
			bad(w, errors.New("请先完成告警配置、模型测试和执行端健康基线采集"))
			return
		}
		ws.OnboardingComplete = true
		next := clone(h.state)
		next.Workspaces[ws.ID] = ws
		if err := h.saveLocked(next); err != nil {
			bad(w, err)
			return
		}
		jsonResponse(w, 200, map[string]any{"workspace": ws})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			p := principal(r)
			if p.Workspace == nil {
				jsonResponse(w, 409, map[string]string{"error": "请先创建个人工作空间", "code": "workspace_required"})
				return
			}
			h.mu.Lock()
			s := h.services[p.Workspace.ID]
			h.mu.Unlock()
			s.routes(webDir).ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/connector/") || strings.HasPrefix(r.URL.Path, "/ingest/") {
			token := bearer(r)
			connector := strings.HasPrefix(r.URL.Path, "/connector/")
			h.mu.Lock()
			services := make([]*Service, 0, len(h.services))
			for _, s := range h.services {
				services = append(services, s)
			}
			h.mu.Unlock()
			for _, s := range services {
				if s.authenticated(token, connector) {
					s.routes(webDir).ServeHTTP(w, r)
					return
				}
			}
			jsonResponse(w, 401, map[string]string{"error": "接入凭证无效或所属工作空间尚未激活"})
			return
		}
		serveWeb(webDir, w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		origin := h.options.PublicOrigin
		if origin == "" {
			host := r.Host
			if v, _, err := net.SplitHostPort(host); err == nil {
				host = v
			}
			ip := net.ParseIP(host)
			if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				jsonResponse(w, 403, map[string]string{"error": "外部访问需要配置 PLATFORM_PUBLIC_ORIGIN"})
				return
			}
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			origin = scheme + "://" + r.Host
		} else {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				jsonResponse(w, 403, map[string]string{"error": "访问地址与 PLATFORM_PUBLIC_ORIGIN 不一致"})
				return
			}
		}
		api := strings.HasPrefix(r.URL.Path, "/api/")
		if api && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			jsonResponse(w, 403, map[string]string{"error": "不允许跨站访问"})
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if o := r.Header.Get("Origin"); o != "" && o != origin {
				jsonResponse(w, 403, map[string]string{"error": "不允许跨站写入"})
				return
			}
			if api && r.Header.Get("X-Requested-With") != "KubePilot" {
				jsonResponse(w, 403, map[string]string{"error": "写操作需要应用请求标识"})
				return
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				jsonResponse(w, 415, map[string]string{"error": "需要 JSON 请求"})
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/auth/") || strings.HasPrefix(r.URL.Path, "/api/workspace/") {
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		}
		public := (r.Method == "GET" && (r.URL.Path == "/api/platform/ping" || r.URL.Path == "/api/auth/session")) || (r.Method == "POST" && (r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/register"))
		if api && !public {
			token := h.token(r)
			a, ws, ok := h.identity(token)
			if !ok {
				jsonResponse(w, 401, map[string]string{"error": "登录已失效，请重新登录"})
				return
			}
			if expected := r.Header.Get("X-KubePilot-Account"); expected != "" && expected != a.ID {
				jsonResponse(w, 409, map[string]string{"error": "账号状态已改变，请重新登录", "code": "session_changed"})
				return
			}
			if expected := r.Header.Get("X-KubePilot-Workspace"); expected != "" && (ws == nil || expected != ws.ID) {
				jsonResponse(w, 409, map[string]string{"error": "工作空间状态已改变，请重新登录", "code": "session_changed"})
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), identityContextKey{}, requestIdentity{Account: a, Workspace: ws, Token: token}))
		}
		mux.ServeHTTP(w, r)
	})
}
