# Mock NVIDIA GPU Device Plugin

This project provides a Kubernetes device plugin that advertises fake `nvidia.com/gpu` resources for scheduling and integration tests. It does not create real GPU device nodes or provide CUDA access; it only makes kubelet believe GPUs are allocatable and injects predictable environment variables during `Allocate`.

## What it does

- Exposes a configurable number of healthy mock GPUs on every node where the DaemonSet runs.
- Registers the standard `nvidia.com/gpu` extended resource by default.
- Labels each node running the plugin with `nvidia.com/gpu.present=true` by default.
- Returns stable fake device IDs like `mock-gpu-0` and sets `NVIDIA_VISIBLE_DEVICES` for requested containers.
- Re-registers with kubelet after kubelet socket recreation.

## Build and test

```bash
make test
make build
docker build -t ghcr.io/<owner>/mock-nvidia-gpu-device-plugin:latest .
```

## Configuration

The binary accepts flags or environment variables:

- `--resource-name` or `RESOURCE_NAME` (default `nvidia.com/gpu`)
- `--device-count` or `DEVICE_COUNT` (default `8`)
- `--device-prefix` or `DEVICE_PREFIX` (default `mock-gpu`)
- `--plugin-dir` or `PLUGIN_DIR` (default `/var/lib/kubelet/device-plugins`)
- `--socket-name` or `SOCKET_NAME` (default `mock-nvidia-gpu.sock`)
- `--kubelet-socket` or `KUBELET_SOCKET` (default `<plugin-dir>/kubelet.sock`)
- `--node-name` or `NODE_NAME` (default empty, disables node labeling when unset)
- `--node-label-key` or `NODE_LABEL_KEY` (default `nvidia.com/gpu.present`)
- `--node-label-value` or `NODE_LABEL_VALUE` (default `true`)

## Deploy

1. Push to `main` or create a `v*` tag to publish via GitHub Actions, or build and push manually.
2. Update the image reference in `deployments/daemonset.yaml`.
3. Apply the DaemonSet:

```bash
kubectl apply -f deployments/daemonset.yaml
```

4. Confirm the resource and label are present on a node:

```bash
kubectl get nodes -o custom-columns=NAME:.metadata.name,GPUS:.status.allocatable.nvidia\\.com/gpu,GPU_PRESENT:.metadata.labels.nvidia\\.com/gpu\\.present
```

5. Run the sample consumer pod:

```bash
kubectl apply -f deployments/example-pod.yaml
kubectl logs -f pod/mock-gpu-consumer
```

The sample pod includes:

```yaml
nodeSelector:
  nvidia.com/gpu.present: "true"
```

If registration keeps retrying with `dial kubelet socket: context deadline exceeded`, verify the node's kubelet actually exposes its device plugin socket at `/var/lib/kubelet/device-plugins/kubelet.sock`. Some distributions use a different kubelet root directory; in that case, set `KUBELET_SOCKET` to the host path mounted into the container.

## Limitations

- No `/dev/nvidia*` device files are mounted into containers.
- No GPU runtime hooks, CUDA libraries, or MIG behavior are provided.
- Workloads that require a real NVIDIA driver stack will still fail.
