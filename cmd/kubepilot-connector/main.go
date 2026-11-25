package main

import (
	"context"
	"encoding/json"
	"flag"
	"kubeagent/internal/connector"
	"kubeagent/internal/core"
	"kubeagent/internal/platform"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	config := flag.String("config", "connector.json", "private enrollment configuration file")
	data := flag.String("data", "data/connector", "persistent data directory")
	demo := flag.Bool("demo", false, "use explicitly labelled simulated Kubernetes provider")
	fault := flag.String("demo-fault", "", "inject one simulated fault at startup (demo only)")
	flag.Parse()
	b, err := os.ReadFile(*config)
	if err != nil {
		slog.Error("cannot read connector configuration")
		os.Exit(1)
	}
	var c platform.ConnectorConfig
	if json.Unmarshal(b, &c) != nil {
		slog.Error("invalid connector configuration")
		os.Exit(1)
	}
	w, err := connector.New(c, *data, *demo)
	if err != nil {
		slog.Error("connector startup failed", "error", err)
		os.Exit(1)
	}
	if *demo && *fault != "" {
		if err = w.Provider.(core.DemoControl).Inject(*fault); err != nil {
			slog.Error("simulation failed", "error", err)
			os.Exit(1)
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	slog.Info("connector started", "provider", w.Kind, "namespace", c.Scope.Namespace)
	_ = w.Run(ctx, func(err error) { slog.Warn("connector status", "error", err) })
}
