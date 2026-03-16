package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	DefaultPluginDir     = pluginapi.DevicePluginPath
	registerRetryPeriod  = 5 * time.Second
	selfDialTimeout      = 5 * time.Second
	kubeletDialTimeout   = 10 * time.Second
	allocationAnnotation = "mock-nvidia-gpu-device-plugin/device-ids"
)

type Config struct {
	ResourceName string
	DeviceCount  int
	DevicePrefix string
	PluginDir    string
	SocketName   string
}

type Server struct {
	pluginapi.UnimplementedDevicePluginServer

	cfg           Config
	logger        *slog.Logger
	socketPath    string
	kubeletSocket string

	mu         sync.RWMutex
	devices    []*pluginapi.Device
	deviceIDs  map[string]struct{}
	grpcServer *grpc.Server
	listener   net.Listener
}

func New(cfg Config, logger *slog.Logger) *Server {
	devices := makeDevices(cfg.DevicePrefix, cfg.DeviceCount)
	deviceIDs := make(map[string]struct{}, len(devices))
	for _, device := range devices {
		deviceIDs[device.ID] = struct{}{}
	}

	return &Server{
		cfg:           cfg,
		logger:        logger.With("component", "mock-device-plugin"),
		socketPath:    filepath.Join(cfg.PluginDir, cfg.SocketName),
		kubeletSocket: filepath.Join(cfg.PluginDir, pluginapi.KubeletSocket),
		devices:       devices,
		deviceIDs:     deviceIDs,
	}
}

func (s *Server) Run(ctx context.Context) error {
	for {
		if err := s.serve(); err != nil {
			return err
		}

		if err := s.register(ctx); err != nil {
			s.stop()
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}

		reason, err := s.watch(ctx)
		s.stop()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}

		s.logger.Info("restarting plugin server", "reason", reason)
	}
}

func (s *Server) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{
		GetPreferredAllocationAvailable: true,
		PreStartRequired:                false,
	}, nil
}

func (s *Server) ListAndWatch(_ *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	s.mu.RLock()
	response := &pluginapi.ListAndWatchResponse{Devices: cloneDevices(s.devices)}
	s.mu.RUnlock()

	if err := stream.Send(response); err != nil {
		return err
	}

	<-stream.Context().Done()
	return nil
}

func (s *Server) GetPreferredAllocation(_ context.Context, req *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	resp := &pluginapi.PreferredAllocationResponse{
		ContainerResponses: make([]*pluginapi.ContainerPreferredAllocationResponse, 0, len(req.ContainerRequests)),
	}

	for _, container := range req.ContainerRequests {
		size := int(container.AllocationSize)
		if size > len(container.AvailableDeviceIDs) {
			return nil, fmt.Errorf("allocation size %d exceeds available devices %d", size, len(container.AvailableDeviceIDs))
		}

		deviceIDs := append([]string{}, container.MustIncludeDeviceIDs...)
		for _, candidate := range container.AvailableDeviceIDs {
			if len(deviceIDs) == size {
				break
			}
			if slices.Contains(deviceIDs, candidate) {
				continue
			}
			deviceIDs = append(deviceIDs, candidate)
		}

		resp.ContainerResponses = append(resp.ContainerResponses, &pluginapi.ContainerPreferredAllocationResponse{
			DeviceIDs: deviceIDs,
		})
	}

	return resp, nil
}

func (s *Server) Allocate(_ context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	resp := &pluginapi.AllocateResponse{
		ContainerResponses: make([]*pluginapi.ContainerAllocateResponse, 0, len(req.ContainerRequests)),
	}

	for _, container := range req.ContainerRequests {
		if err := s.validateDeviceIDs(container.DevicesIds); err != nil {
			return nil, err
		}

		visible := strings.Join(container.DevicesIds, ",")
		resp.ContainerResponses = append(resp.ContainerResponses, &pluginapi.ContainerAllocateResponse{
			Envs: map[string]string{
				"NVIDIA_VISIBLE_DEVICES":      visible,
				"MOCK_NVIDIA_VISIBLE_DEVICES": visible,
				"MOCK_NVIDIA_RESOURCE_NAME":   s.cfg.ResourceName,
				"MOCK_NVIDIA_GPU_COUNT":       strconv.Itoa(len(container.DevicesIds)),
			},
			Annotations: map[string]string{
				allocationAnnotation: visible,
			},
		})
	}

	return resp, nil
}

func (s *Server) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}

func (s *Server) serve() error {
	if err := os.MkdirAll(s.cfg.PluginDir, 0o755); err != nil {
		return fmt.Errorf("create plugin directory: %w", err)
	}
	if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on plugin socket: %w", err)
	}

	grpcServer := grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(grpcServer, s)

	s.mu.Lock()
	s.listener = listener
	s.grpcServer = grpcServer
	s.mu.Unlock()

	go func() {
		if err := grpcServer.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) {
			s.logger.Error("gRPC server stopped unexpectedly", "error", err)
		}
	}()

	if err := s.waitForSelfDial(); err != nil {
		s.stop()
		return err
	}

	s.logger.Info("plugin server listening", "socket", s.socketPath, "resource_name", s.cfg.ResourceName, "device_count", len(s.devices))
	return nil
}

func (s *Server) register(ctx context.Context) error {
	request := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     s.cfg.SocketName,
		ResourceName: s.cfg.ResourceName,
		Options: &pluginapi.DevicePluginOptions{
			GetPreferredAllocationAvailable: true,
		},
	}

	for {
		if err := s.registerOnce(ctx, request); err == nil {
			s.logger.Info("registered with kubelet", "kubelet_socket", s.kubeletSocket)
			return nil
		} else if errors.Is(err, context.Canceled) {
			return err
		} else {
			s.logger.Info("registration failed, retrying", "error", err, "retry_in", registerRetryPeriod.String())
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(registerRetryPeriod):
		}
	}
}

func (s *Server) registerOnce(ctx context.Context, request *pluginapi.RegisterRequest) error {
	dialCtx, cancel := context.WithTimeout(ctx, kubeletDialTimeout)
	defer cancel()

	conn, err := grpc.DialContext(
		dialCtx,
		s.kubeletSocket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(unixDialer),
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("dial kubelet socket: %w", err)
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	if _, err := client.Register(dialCtx, request); err != nil {
		return fmt.Errorf("register device plugin: %w", err)
	}

	return nil
}

func (s *Server) watch(ctx context.Context) (string, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return "", fmt.Errorf("create fsnotify watcher: %w", err)
	}
	defer watcher.Close()

	if err := watcher.Add(s.cfg.PluginDir); err != nil {
		return "", fmt.Errorf("watch plugin directory: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err := <-watcher.Errors:
			return "", fmt.Errorf("watch plugin directory: %w", err)
		case event := <-watcher.Events:
			switch {
			case samePath(event.Name, s.kubeletSocket) && event.Has(fsnotify.Create):
				return "kubelet socket recreated", nil
			case samePath(event.Name, s.socketPath) && (event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename)):
				return "plugin socket removed", nil
			}
		}
	}
}

func (s *Server) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.grpcServer != nil {
		s.grpcServer.Stop()
		s.grpcServer = nil
	}
	if s.listener != nil {
		_ = s.listener.Close()
		s.listener = nil
	}
	_ = os.Remove(s.socketPath)
}

func (s *Server) validateDeviceIDs(deviceIDs []string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, id := range deviceIDs {
		if _, ok := s.deviceIDs[id]; !ok {
			return fmt.Errorf("unknown mock device %q", id)
		}
	}
	return nil
}

func (s *Server) waitForSelfDial() error {
	deadline := time.Now().Add(selfDialTimeout)
	for {
		conn, err := net.DialTimeout("unix", s.socketPath, 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait for plugin socket readiness: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func makeDevices(prefix string, count int) []*pluginapi.Device {
	devices := make([]*pluginapi.Device, 0, count)
	for idx := 0; idx < count; idx++ {
		devices = append(devices, &pluginapi.Device{
			ID:     fmt.Sprintf("%s-%d", prefix, idx),
			Health: pluginapi.Healthy,
		})
	}
	return devices
}

func cloneDevices(devices []*pluginapi.Device) []*pluginapi.Device {
	cloned := make([]*pluginapi.Device, 0, len(devices))
	for _, device := range devices {
		copy := *device
		cloned = append(cloned, &copy)
	}
	return cloned
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func unixDialer(ctx context.Context, target string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", target)
}
