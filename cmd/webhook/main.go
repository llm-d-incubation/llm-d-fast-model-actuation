package main

import (
	"flag"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	klog "k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		klog.Fatal(err)
	}

	// validator := &pod.PodAnnotationValidator{}
	// mgr.GetWebhookServer().Register("/validate-pods", &webhook.Admission{Handler: validator})

	klog.Info("starting webhook server")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		klog.ErrorS(err, "manager exited non-zero")
		os.Exit(1)
	}

}
