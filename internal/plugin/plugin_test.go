package plugin

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

func TestMakeDevices(t *testing.T) {
	devices := makeDevices("mock-gpu", 3)
	if len(devices) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(devices))
	}

	for idx, device := range devices {
		expectedID := "mock-gpu-" + strconv.Itoa(idx)
		if device.ID != expectedID {
			t.Fatalf("expected device ID %q, got %q", expectedID, device.ID)
		}
		if device.Health != pluginapi.Healthy {
			t.Fatalf("expected healthy device, got %q", device.Health)
		}
	}
}

func TestAllocateSetsExpectedEnvironment(t *testing.T) {
	server := New(Config{
		ResourceName: "nvidia.com/gpu",
		DeviceCount:  2,
		DevicePrefix: "mock-gpu",
		PluginDir:    t.TempDir(),
		SocketName:   "mock.sock",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	resp, err := server.Allocate(context.Background(), &pluginapi.AllocateRequest{
		ContainerRequests: []*pluginapi.ContainerAllocateRequest{
			{DevicesIds: []string{"mock-gpu-0", "mock-gpu-1"}},
		},
	})
	if err != nil {
		t.Fatalf("allocate returned error: %v", err)
	}

	if len(resp.ContainerResponses) != 1 {
		t.Fatalf("expected 1 container response, got %d", len(resp.ContainerResponses))
	}

	envs := resp.ContainerResponses[0].Envs
	if got, want := envs["NVIDIA_VISIBLE_DEVICES"], "mock-gpu-0,mock-gpu-1"; got != want {
		t.Fatalf("expected NVIDIA_VISIBLE_DEVICES=%q, got %q", want, got)
	}
	if got, want := envs["MOCK_NVIDIA_GPU_COUNT"], "2"; got != want {
		t.Fatalf("expected MOCK_NVIDIA_GPU_COUNT=%q, got %q", want, got)
	}
}

func TestAllocateRejectsUnknownDevice(t *testing.T) {
	server := New(Config{
		ResourceName: "nvidia.com/gpu",
		DeviceCount:  1,
		DevicePrefix: "mock-gpu",
		PluginDir:    t.TempDir(),
		SocketName:   "mock.sock",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := server.Allocate(context.Background(), &pluginapi.AllocateRequest{
		ContainerRequests: []*pluginapi.ContainerAllocateRequest{
			{DevicesIds: []string{"missing-device"}},
		},
	})
	if err == nil {
		t.Fatal("expected allocate to reject unknown device ID")
	}
}

func TestNewDefaultsKubeletSocketToPluginDir(t *testing.T) {
	dir := t.TempDir()
	server := New(Config{
		ResourceName: "nvidia.com/gpu",
		DeviceCount:  1,
		DevicePrefix: "mock-gpu",
		PluginDir:    dir,
		SocketName:   "mock.sock",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	want := filepath.Join(dir, "kubelet.sock")
	if server.kubeletSocket != want {
		t.Fatalf("expected kubelet socket %q, got %q", want, server.kubeletSocket)
	}
}

func TestValidateSocketPathRejectsNonSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	err := validateSocketPath(path)
	if err == nil {
		t.Fatal("expected validateSocketPath to reject non-socket")
	}
}
