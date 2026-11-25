package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"kubeagent/internal/core"
	"kubeagent/internal/providers"
	"kubeagent/internal/server"
)

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func main() {
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	mode := env("AGENT_MODE", "demo")
	namespace := env("AGENT_NAMESPACE", "kube-agent-lab")
	dataDir := env("AGENT_DATA_DIR", "data")
	addr := env("AGENT_ADDR", "127.0.0.1:18080")
	interval, err := strconv.Atoi(env("AGENT_INTERVAL_SECONDS", "30"))
	if err != nil || interval < 5 || interval > 86400 {
		return errors.New("AGENT_INTERVAL_SECONDS 必须在 5 到 86400 之间")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	token := os.Getenv("AGENT_API_TOKEN")
	if (ip == nil || !ip.IsLoopback()) && token == "" {
		return errors.New("非本机监听需要配置 AGENT_API_TOKEN")
	}
	var provider core.Provider
	var capture func(context.Context, string, string) error
	if mode == "demo" {
		provider, err = providers.NewDemo(filepath.Join(dataDir, "demo-world.json"), namespace)
	} else if mode == "kubernetes" {
		var kube *providers.Kubernetes
		kube, err = providers.NewKubernetes(providers.KubeConfig{Kubeconfig: os.Getenv("AGENT_KUBECONFIG"), Context: os.Getenv("AGENT_KUBE_CONTEXT"), Namespace: namespace, BaselinePath: filepath.Join(dataDir, "baselines.json"), ProbeConfig: os.Getenv("AGENT_PROBE_CONFIG")})
		if err == nil {
			provider = kube
			capture = kube.CaptureBaseline
		}
	} else {
		return errors.New("AGENT_MODE 只支持 demo 或 kubernetes")
	}
	if err != nil {
		return err
	}
	var diagnoser core.Diagnoser
	var agentModel core.AgentModel
	model := os.Getenv("AGENT_OLLAMA_MODEL")
	if model != "" {
		diagnoser = &providers.Ollama{URL: env("AGENT_OLLAMA_URL", "http://127.0.0.1:11434"), Model: model}
	}
	if os.Getenv("AGENT_LLM_MODEL") != "" || os.Getenv("AGENT_LLM_BASE_URL") != "" {
		compatible, configErr := providers.NewCompatible(os.Getenv("AGENT_LLM_BASE_URL"), os.Getenv("AGENT_LLM_MODEL"), os.Getenv("AGENT_LLM_API_KEY"))
		if configErr != nil {
			return configErr
		}
		diagnoser, agentModel, model = compatible, compatible, compatible.Model
	}
	settings := core.Settings{Mode: mode, Namespace: namespace, WritesEnabled: mode == "demo" || strings.EqualFold(os.Getenv("AGENT_ENABLE_WRITES"), "true"), IntervalSeconds: interval, ModelConfigured: diagnoser != nil, Model: model, Storage: "本地 JSON · 原子保存", Version: "0.1.0"}
	settings.AgentModel = "rules"
	if agentModel != nil {
		settings.AgentModel = agentModel.Name()
	}
	engine, err := core.NewEngine(core.FileStore{Path: filepath.Join(dataDir, mode+"-state.json")}, provider, diagnoser, settings)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	agent := core.NewAgentRunner(ctx, engine, agentModel, 180*time.Second)
	defer agent.Close()
	go func() {
		inspectCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		if err := engine.Inspect(inspectCtx, "startup"); err != nil {
			slog.Warn("initial inspection", "error", err)
		}
		cancel()
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				taskCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
				if err := agent.Tick(taskCtx); err != nil {
					slog.Warn("scheduled inspection", "error", err)
				}
				cancel()
			}
		}
	}()
	var captureWithAudit func(context.Context, string, string) error
	if capture != nil {
		captureWithAudit = func(ctx context.Context, kind, name string) error {
			return engine.CaptureBaseline(ctx, kind, name, capture)
		}
	}
	api := server.API{Engine: engine, Agent: agent, WebDir: env("AGENT_WEB_DIR", "web/dist"), Token: token, CaptureBaseline: captureWithAudit}
	httpServer := &http.Server{Addr: addr, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	slog.Info("KubePilot ready", "url", "http://"+addr, "mode", mode, "namespace", namespace, "writes", settings.WritesEnabled)
	err = httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
