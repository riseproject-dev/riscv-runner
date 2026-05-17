package plugin

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	resourceName  = "riseproject.com/runner"
	socketName    = "rise-riscv-runner.sock"
	socketDir     = "/var/lib/kubelet/device-plugins"
	kubeletSocket = "/var/lib/kubelet/device-plugins/kubelet.sock"
)

// Plugin implements the Kubernetes device plugin interface.
// It advertises a single "riseproject.com/runner" device per node
// to control CI job concurrency.
type Plugin struct {
	server *grpc.Server
	socket string
	stop   chan struct{}
}

func New() *Plugin {
	return &Plugin{
		socket: filepath.Join(socketDir, socketName),
		stop:   make(chan struct{}),
	}
}

func (p *Plugin) Start() error {
	if err := p.serve(); err != nil {
		return err
	}
	if err := p.register(); err != nil {
		p.server.Stop()
		return err
	}

	go p.watchKubelet()

	klog.Infof("Device plugin started, serving resource %s", resourceName)
	return nil
}

func (p *Plugin) Stop() {
	close(p.stop)
	if p.server != nil {
		p.server.Stop()
	}
	os.Remove(p.socket)
}

func (p *Plugin) serve() error {
	os.Remove(p.socket)

	lis, err := net.Listen("unix", p.socket)
	if err != nil {
		return err
	}

	p.server = grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(p.server, p)

	go func() {
		if err := p.server.Serve(lis); err != nil {
			klog.Errorf("gRPC server exited with error: %v", err)
		}
	}()

	// Wait briefly to ensure server is listening
	time.Sleep(100 * time.Millisecond)
	return nil
}

func (p *Plugin) register() error {
	conn, err := grpc.NewClient("unix://"+kubeletSocket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := pluginapi.NewRegistrationClient(conn)
	_, err = client.Register(ctx, &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     socketName,
		ResourceName: resourceName,
	})
	return err
}

// watchKubelet monitors the kubelet socket directory. When kubelet restarts
// and recreates its socket, the plugin re-registers itself.
func (p *Plugin) watchKubelet() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		klog.Errorf("Failed to create fsnotify watcher: %v", err)
		return
	}
	defer watcher.Close()

	if err := watcher.Add(socketDir); err != nil {
		klog.Errorf("Failed to watch %s: %v", socketDir, err)
		return
	}

	for {
		select {
		case event := <-watcher.Events:
			if event.Name == kubeletSocket && event.Has(fsnotify.Create) {
				klog.Info("Kubelet restarted, re-registering device plugin...")
				time.Sleep(time.Second)
				p.server.Stop()
				if err := p.serve(); err != nil {
					klog.Errorf("Failed to restart gRPC server: %v", err)
					continue
				}
				if err := p.register(); err != nil {
					klog.Errorf("Failed to re-register: %v", err)
				}
			}
		case err := <-watcher.Errors:
			klog.Errorf("fsnotify error: %v", err)
		case <-p.stop:
			return
		}
	}
}

// --- Device Plugin gRPC interface ---

func (p *Plugin) GetDevicePluginOptions(_ context.Context, _ *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{}, nil
}

func (p *Plugin) ListAndWatch(_ *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	// Advertise exactly one healthy device to limit concurrency to 1 job per node
	if err := stream.Send(&pluginapi.ListAndWatchResponse{
		Devices: []*pluginapi.Device{
			{
				ID:     "runner-0",
				Health: pluginapi.Healthy,
			},
		},
	}); err != nil {
		return err
	}

	// Block until the plugin is stopped
	<-p.stop
	return nil
}

func (p *Plugin) Allocate(_ context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	responses := make([]*pluginapi.ContainerAllocateResponse, len(req.ContainerRequests))
	for i := range req.ContainerRequests {
		responses[i] = &pluginapi.ContainerAllocateResponse{}
	}
	return &pluginapi.AllocateResponse{ContainerResponses: responses}, nil
}

func (p *Plugin) GetPreferredAllocation(_ context.Context, _ *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	return &pluginapi.PreferredAllocationResponse{}, nil
}

func (p *Plugin) PreStartContainer(_ context.Context, _ *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}
