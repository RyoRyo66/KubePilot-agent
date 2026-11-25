package server

import (
	"kubeagent/internal/core"
	"net/http"
)

func (a API) agentRoutes(mux *http.ServeMux) {
	available := func(w http.ResponseWriter) bool {
		if a.Agent != nil {
			return true
		}
		writeJSON(w, 503, map[string]string{"error": "Agent 执行器尚未启动"})
		return false
	}
	mux.HandleFunc("GET /api/agent/runs", func(w http.ResponseWriter, r *http.Request) {
		if available(w) {
			writeJSON(w, 200, a.Engine.State().AgentRuns)
		}
	})
	mux.HandleFunc("GET /api/agent/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !available(w) {
			return
		}
		run, err := a.Agent.Get(r.PathValue("id"))
		if err != nil {
			failure(w, err)
			return
		}
		writeJSON(w, 200, run)
	})
	mux.HandleFunc("POST /api/agent/runs", func(w http.ResponseWriter, r *http.Request) {
		if !available(w) {
			return
		}
		var body core.AgentRequest
		if err := decode(w, r, &body); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		run, err := a.Agent.Start(body, "operator")
		if err != nil {
			_ = a.Engine.RecordRejected(r.URL.Path, err)
			failure(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, run)
	})
	mux.HandleFunc("POST /api/agent/runs/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		if !available(w) {
			return
		}
		var body struct {
			PlanHash string `json:"planHash"`
		}
		if err := decode(w, r, &body); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := a.Agent.Approve(r.PathValue("id"), body.PlanHash); err != nil {
			_ = a.Engine.RecordRejected(r.URL.Path, err)
			failure(w, err)
			return
		}
		run, _ := a.Agent.Get(r.PathValue("id"))
		writeJSON(w, http.StatusAccepted, run)
	})
	mux.HandleFunc("POST /api/agent/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !available(w) {
			return
		}
		var body struct{}
		if err := decode(w, r, &body); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if err := a.Agent.Cancel(r.PathValue("id")); err != nil {
			failure(w, err)
			return
		}
		run, _ := a.Agent.Get(r.PathValue("id"))
		writeJSON(w, 200, run)
	})
}
