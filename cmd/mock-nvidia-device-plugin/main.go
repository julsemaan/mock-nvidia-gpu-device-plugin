package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"mock-nvidia-gpu-device-plugin/internal/plugin"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := parseConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	server := plugin.New(cfg, logger)
	if err := server.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("plugin exited with error", "error", err)
		os.Exit(1)
	}
}

func parseConfig() (plugin.Config, error) {
	cfg := plugin.Config{}

	flag.StringVar(&cfg.ResourceName, "resource-name", envString("RESOURCE_NAME", "nvidia.com/gpu"), "resource name advertised to kubelet")
	flag.IntVar(&cfg.DeviceCount, "device-count", envInt("DEVICE_COUNT", 8), "number of mock GPUs exposed on each node")
	flag.StringVar(&cfg.DevicePrefix, "device-prefix", envString("DEVICE_PREFIX", "mock-gpu"), "prefix used when generating fake device IDs")
	flag.StringVar(&cfg.PluginDir, "plugin-dir", envString("PLUGIN_DIR", plugin.DefaultPluginDir), "kubelet device plugin directory on the host")
	flag.StringVar(&cfg.SocketName, "socket-name", envString("SOCKET_NAME", "mock-nvidia-gpu.sock"), "unix socket filename used by the plugin")
	flag.Parse()

	if cfg.ResourceName == "" {
		return cfg, fmt.Errorf("resource-name must not be empty")
	}
	if cfg.DevicePrefix == "" {
		return cfg, fmt.Errorf("device-prefix must not be empty")
	}
	if cfg.PluginDir == "" {
		return cfg, fmt.Errorf("plugin-dir must not be empty")
	}
	if cfg.SocketName == "" {
		return cfg, fmt.Errorf("socket-name must not be empty")
	}
	if cfg.DeviceCount < 0 {
		return cfg, fmt.Errorf("device-count must be zero or greater")
	}

	return cfg, nil
}

func envString(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
