package poolmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"

	api "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/api"
)

const ManagerName = "pool-manager"

type Manager struct {
	coreclient coreclient.CoreV1Interface
	namespace  string
	poolSize   int
}

func NewPoolManager(logger klog.Logger, coreClient coreclient.CoreV1Interface, namespace string, poolSize int) (*Manager, error) {
	return &Manager{
		coreclient: coreClient,
		namespace:  namespace,
		poolSize:   poolSize,
	}, nil
}

func (mgr *Manager) reconcilePool() error {
	ctx := context.Background()
	pods, err := mgr.coreclient.Pods(mgr.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "llm-d.ai/role=server-running",
	})
	if err != nil {
		return err
	}

	idleCount := 0
	for _, p := range pods.Items {
		if p.Annotations["pool.llm-d.ai/state"] == "idle" {
			idleCount++
		}
	}

	for i := idleCount; i < mgr.poolSize; i++ {
		if err := mgr.createIdlePod(ctx); err != nil {
			log.Printf("failed to create idle launcher pod: %v", err)
		}
	}

	return nil
}

func (mgr *Manager) createIdlePod(ctx context.Context) error {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "vllm-pool-",
			Namespace:    mgr.namespace,
			Labels: map[string]string{
				"llm-d.ai/role": "server-running",
			},
			Annotations: map[string]string{
				"pool.llm-d.ai/state": "idle",
			},
		},
		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyAlways,
			Containers: []corev1.Container{
				{
					Name:  "launcher",
					Image: "org/vllm-launcher:latest", //TODO
					Ports: []corev1.ContainerPort{
						{ContainerPort: 8001},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "launcher-config",
							MountPath: "/etc/launcher",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "launcher-config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "launcher-config",
							},
						},
					},
				},
			},
		},
	}

	_, err := mgr.coreclient.Pods(mgr.namespace).Create(ctx, pod, metav1.CreateOptions{})
	return err
}

func (mgr *Manager) pickIdlePod(ctx context.Context) (*v1.Pod, error) {
	pods, err := mgr.coreclient.Pods(mgr.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "llm-d.ai/role=server-running",
	})
	if err != nil {
		return nil, err
	}

	for _, p := range pods.Items {
		if p.Annotations["pool.llm-d.ai/state"] == "idle" {
			return &p, nil
		}
	}
	return nil, fmt.Errorf("no idle pods available")
}

func (mgr *Manager) ActivatePod(ctx context.Context, podName, serverPatch, pool string) error {
	pod, err := mgr.coreclient.Pods(mgr.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	launcherURL := fmt.Sprintf("http://%s:8001/v1/vllm", pod.Status.PodIP) // TODO

	resp, err := http.Post(launcherURL, "application/json", bytes.NewBuffer([]byte(serverPatch)))
	if err != nil {
		return fmt.Errorf("failed POST to launcher: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("launcher returned status %d", resp.StatusCode)
	}

	patch := []byte(fmt.Sprintf(`{
		"metadata": {
			"labels": {
				"inference.k8s.io/pool": "%s"
			},
			"annotations": {
				"pool.llm-d.ai/state": "active",
				"%s": %q
			}
		}
	}`, pool, api.ServerPatchAnnotationName, serverPatch))

	_, err = mgr.coreclient.Pods(mgr.namespace).Patch(ctx, podName, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch pod labels/annotations: %w", err)
	}

	return nil
}

func (mgr *Manager) ResetPod(ctx context.Context, podName string) error {
	pod, err := mgr.coreclient.Pods(mgr.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	launcherURL := fmt.Sprintf("http://%s:8001/v1/vllm", pod.Status.PodIP) // TODO

	req, _ := http.NewRequest(http.MethodDelete, launcherURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("failed DELETE to launcher: %w", err)
	}
	defer resp.Body.Close()

	patch := []byte(`{
		"metadata": {
			"labels": {
				"inference.k8s.io/pool": null,
				"inference.k8s.io/model": null
			},
			"annotations": {
				"pool.llm-d.ai/state": "idle",
				"dual-pod.llm-d.ai/server-patch": null
			}
		}
	}`)

	_, err = mgr.coreclient.Pods(mgr.namespace).Patch(ctx, podName, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch pod back to idle: %w", err)
	}

	return nil
}

type allocateRequest struct {
	PodName     string `json:"podName"`
	Pool        string `json:"pool"`
	ServerPatch string `json:"serverPatch"`
}

func (mgr *Manager) handleAllocate(w http.ResponseWriter, r *http.Request) {
	var req allocateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	var podName string
	if req.PodName != "" {
		podName = req.PodName
	} else {
		pod, err := mgr.pickIdlePod(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		podName = pod.Name
	}

	if err := mgr.ActivatePod(ctx, podName, req.ServerPatch, req.Pool); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

type deleteRequest struct {
	PodName string `json:"podName"`
}

func (mgr *Manager) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req deleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	if err := mgr.ResetPod(ctx, req.PodName); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (mgr *Manager) Serve(port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/allocate", mgr.handleAllocate)
	mux.HandleFunc("/delete", mgr.handleDelete)

	go func() {
		for {
			if err := mgr.reconcilePool(); err != nil {
				log.Printf("reconcile error: %v", err)
			}
			time.Sleep(10 * time.Second)
		}
	}()

	addr := fmt.Sprintf(":%d", port)
	log.Printf("Pool Manager HTTP server listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}
