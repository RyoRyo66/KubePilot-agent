package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/term"
	"io"
	"kubeagent/internal/platform"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func main() {
	if err := run(); err != nil {
		slog.Error("platform stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) > 1 {
		return maintain(os.Args[1:])
	}
	addr := env("PLATFORM_ADDR", "127.0.0.1:18081")
	origin := strings.TrimRight(os.Getenv("PLATFORM_PUBLIC_ORIGIN"), "/")
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if (ip == nil || !ip.IsLoopback()) && origin == "" {
		return errors.New("外部监听必须设置 PLATFORM_PUBLIC_ORIGIN；通过 HTTPS 反向代理访问平台")
	}
	if origin != "" {
		if err := platform.ValidatePublicURL(origin, os.Getenv("PLATFORM_ALLOW_HTTP") == "true"); err != nil {
			return err
		}
	}
	s, err := platform.NewHub(env("PLATFORM_DATA_DIR", "data/platform"), platform.HubOptions{PublicOrigin: origin, RegistrationClosed: os.Getenv("PLATFORM_REGISTRATION_CLOSED") == "true"})
	if err != nil {
		return err
	}
	defer s.Close()
	s.Run()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := &http.Server{Addr: addr, Handler: s.Handler(env("PLATFORM_WEB_DIR", "web/dist")), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		c, end := context.WithTimeout(context.Background(), 5*time.Second)
		defer end()
		_ = server.Shutdown(c)
	}()
	slog.Info("KubePilot B/S platform ready", "url", "http://"+addr, "version", platform.Version)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func maintain(args []string) error {
	if args[0] != "reset-password" && args[0] != "claim-legacy" {
		return errors.New("维护命令：reset-password --username 用户名 或 claim-legacy --username 用户名；执行前请先停止平台")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	username := flags.String("username", "", "目标账号的用户名")
	dataDir := flags.String("data", env("PLATFORM_DATA_DIR", "data/platform"), "平台数据目录")
	stdin := flags.Bool("password-stdin", false, "从标准输入读取两行新密码，仅用于自动化维护")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *username == "" || flags.NArg() != 0 {
		return errors.New("必须指定 --username，密码不能通过命令行参数传入")
	}
	h, err := platform.NewHub(*dataDir, platform.HubOptions{})
	if err != nil {
		return err
	}
	defer h.Close()
	if args[0] == "claim-legacy" {
		if err = h.ClaimLegacy(*username); err != nil {
			return err
		}
		fmt.Println("旧数据已分配给指定账号；自动修复已暂停，请登录后核对授权配置。")
		return nil
	}
	var first, second string
	if *stdin {
		b, e := io.ReadAll(io.LimitReader(os.Stdin, 2049))
		if e != nil {
			return e
		}
		if len(b) > 2048 {
			return errors.New("密码输入过长")
		}
		lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
		if len(lines) < 2 {
			return errors.New("标准输入需要两行相同的新密码")
		}
		first, second = lines[0], lines[1]
	} else {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("请在交互终端执行密码重置，或显式使用 --password-stdin")
		}
		fmt.Fprint(os.Stderr, "新密码（12–128 个字符）：")
		a, e := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if e != nil {
			return e
		}
		fmt.Fprint(os.Stderr, "再次输入新密码：")
		b, e := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if e != nil {
			return e
		}
		first, second = string(a), string(b)
	}
	if first != second {
		return errors.New("两次输入的密码不一致")
	}
	if err = h.ResetPassword(*username, first); err != nil {
		return err
	}
	fmt.Println("密码已重置，该账号的全部登录会话已失效。")
	return nil
}
