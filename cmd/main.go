/*
Copyright 2025 The OpenSaola Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/tls"
	"flag"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"time"

	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/internal/controller"
	"github.com/OpenSaola/opensaola/pkg/config"
	"github.com/OpenSaola/opensaola/internal/k8s"
	"github.com/OpenSaola/opensaola/internal/resource"
	"github.com/OpenSaola/opensaola/internal/resource/logger"
	"github.com/OpenSaola/opensaola/internal/service/consts"
	"github.com/OpenSaola/opensaola/internal/service/packages"
	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"

	"github.com/go-logr/zerologr"
	"github.com/spf13/viper"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(v1.AddToScheme(scheme))
	v1.AddToScheme(clientgoscheme.Scheme)
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var leaderElectionLeaseDuration time.Duration
	var leaderElectionRenewDeadline time.Duration
	var leaderElectionRetryPeriod time.Duration
	var leaderElectionReleaseOnCancel bool
	var kubeQPS float64
	var kubeBurst int
	var kubeTimeout time.Duration
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8080", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.DurationVar(&leaderElectionLeaseDuration, "leader-elect-lease-duration", 60*time.Second, "The duration that non-leader candidates will wait to force acquire leadership.")
	flag.DurationVar(&leaderElectionRenewDeadline, "leader-elect-renew-deadline", 45*time.Second, "The duration that the acting leader will retry refreshing leadership before giving up.")
	flag.DurationVar(&leaderElectionRetryPeriod, "leader-elect-retry-period", 15*time.Second, "The duration the LeaderElector clients should wait between tries of actions.")
	flag.BoolVar(&leaderElectionReleaseOnCancel, "leader-elect-release-on-cancel", true, "If true, the leader will step down voluntarily when the manager ends.")
	flag.Float64Var(&kubeQPS, "kube-qps", 100, "Kubernetes client QPS.")
	flag.IntVar(&kubeBurst, "kube-burst", 200, "Kubernetes client burst.")
	flag.DurationVar(&kubeTimeout, "kube-timeout", 30*time.Second, "Kubernetes REST client timeout.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	if err := config.Initialize(); err != nil {
		setupLog.Error(err, "failed to initialize config")
		os.Exit(1)
	}
	packages.SetDataNamespace(viper.GetString("data_namespace"))
	resource.Initialize()

	ctrl.SetLogger(zerologr.New(&logger.Log.Zlog).WithCallDepth(-1))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Create watchers for metrics and webhooks certificates
	var metricsCertWatcher, webhookCertWatcher *certwatcher.CertWatcher

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		var err error
		webhookCertWatcher, err = certwatcher.New(
			filepath.Join(webhookCertPath, webhookCertName),
			filepath.Join(webhookCertPath, webhookCertKey),
		)
		if err != nil {
			setupLog.Error(err, "Failed to initialize webhook certificate watcher")
			os.Exit(1)
		}

		webhookTLSOpts = append(webhookTLSOpts, func(config *tls.Config) {
			config.GetCertificate = webhookCertWatcher.GetCertificate
		})
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: webhookTLSOpts,
	})

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(metricsCertPath, metricsCertName),
			filepath.Join(metricsCertPath, metricsCertKey),
		)
		if err != nil {
			setupLog.Error(err, "to initialize metrics certificate watcher", "error", err)
			os.Exit(1)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}

	if enableLeaderElection {
		if leaderElectionRenewDeadline >= leaderElectionLeaseDuration {
			setupLog.Error(nil, "invalid leader election config: renew-deadline must be < lease-duration",
				"leaseDuration", leaderElectionLeaseDuration, "renewDeadline", leaderElectionRenewDeadline)
			os.Exit(1)
		}
		if leaderElectionRetryPeriod >= leaderElectionRenewDeadline {
			setupLog.Error(nil, "invalid leader election config: retry-period must be < renew-deadline",
				"retryPeriod", leaderElectionRetryPeriod, "renewDeadline", leaderElectionRenewDeadline)
			os.Exit(1)
		}
	}

	cfg := ctrl.GetConfigOrDie()
	cfg.QPS = float32(kubeQPS)
	cfg.Burst = kubeBurst
	cfg.Timeout = kubeTimeout

	// Use a dedicated REST config for leader election to avoid sharing rate-limiter/http client with business requests.
	leaderCfg := rest.CopyConfig(cfg)
	leaderCfg.QPS = 5
	leaderCfg.Burst = 10
	leaderCfg.Timeout = kubeTimeout
	if enableLeaderElection && leaderCfg.Timeout < leaderElectionRenewDeadline {
		leaderCfg.Timeout = leaderElectionRenewDeadline
	}

	setupLog.Info("kube client config",
		"qps", cfg.QPS, "burst", cfg.Burst, "timeout", cfg.Timeout,
		"leaderLeaseDuration", leaderElectionLeaseDuration, "leaderRenewDeadline", leaderElectionRenewDeadline, "leaderRetryPeriod", leaderElectionRetryPeriod,
		"leaderReleaseOnCancel", leaderElectionReleaseOnCancel,
	)

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "5056ac73.middleware.cn",
		// Optimize Secret informer: only cache operator-owned Secrets
		// and strip Data field to reduce memory (~200MB savings).
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Secret{}: {
					Label: labels.SelectorFromSet(labels.Set{
						v1.LabelProject: consts.ProjectOpenSaola,
					}),
					Transform: func(obj interface{}) (interface{}, error) {
						if secret, ok := obj.(*corev1.Secret); ok {
							secret.Data = nil
						}
						return obj, nil
					},
				},
			},
		},
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		LeaderElectionConfig:          leaderCfg,
		LeaderElectionReleaseOnCancel: leaderElectionReleaseOnCancel,
		LeaseDuration:                 &leaderElectionLeaseDuration,
		RenewDeadline:                 &leaderElectionRenewDeadline,
		RetryPeriod:                   &leaderElectionRetryPeriod,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// pprof debug endpoint (port 6060).
	go func() {
		setupLog.Info("pprof listening on :6060")
		if err := http.ListenAndServe(":6060", nil); err != nil {
			setupLog.Error(err, "pprof server failed")
		}
	}()

	// Set APIReader so packages.Get() bypasses the cache (which strips Secret.Data).
	packages.SetAPIReader(mgr.GetAPIReader())

	// Set APIReader for status update retries to bypass the informer cache,
	// preventing stale-cache 409 loops under high concurrency.
	k8s.SetStatusAPIReader(mgr.GetAPIReader())

	if err = (&controller.MiddlewareOperatorBaselineReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MiddlewareOperatorBaseline")
		os.Exit(1)
	}
	if err = (&controller.MiddlewareReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Middleware")
		os.Exit(1)
	}
	if err = (&controller.MiddlewareBaselineReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MiddlewareBaseline")
		os.Exit(1)
	}
	if err = (&controller.MiddlewareOperatorReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("middleware-operator-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MiddlewareOperator")
		os.Exit(1)
	}
	if err = (&controller.MiddlewareOperatorRuntimeReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MiddlewareOperatorRuntime")
		os.Exit(1)
	}
	if err = (&controller.MiddlewareConfigurationReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MiddlewareConfiguration")
		os.Exit(1)
	}
	if err = (&controller.MiddlewareActionBaselineReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MiddlewareActionBaseline")
		os.Exit(1)
	}
	if err = (&controller.MiddlewareActionReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MiddlewareAction")
		os.Exit(1)
	}
	if err = (&controller.MiddlewarePackageReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MiddlewarePackage")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if metricsCertWatcher != nil {
		setupLog.Info("Adding metrics certificate watcher to manager")
		if err := mgr.Add(metricsCertWatcher); err != nil {
			setupLog.Error(err, "unable to add metrics certificate watcher to manager")
			os.Exit(1)
		}
	}

	if webhookCertWatcher != nil {
		setupLog.Info("Adding webhook certificate watcher to manager")
		if err := mgr.Add(webhookCertWatcher); err != nil {
			setupLog.Error(err, "unable to add webhook certificate watcher to manager")
			os.Exit(1)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	ctx := ctrl.SetupSignalHandler()
	if err := mgr.Add(&leaderBackgroundTasks{cli: mgr.GetClient(), scheme: mgr.GetScheme(), cfg: cfg}); err != nil {
		setupLog.Error(err, "unable to add leader background tasks")
		os.Exit(1)
	}
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
