// Command vm-fixtures synthesises kube-state-metrics-style and service-graph
// metrics for use in the kube-state-graph integration harness.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type fixtureSet struct {
	Pods  []podFixture            `yaml:"pods"`
	Nodes []nodeFixture           `yaml:"nodes"`
	PVCs  []pvcFixture            `yaml:"pvcs"`
	Calls []serviceGraphFixture   `yaml:"calls"`
}

type podFixture struct {
	Cluster   string `yaml:"cluster"`
	Namespace string `yaml:"namespace"`
	Pod       string `yaml:"pod"`
	UID       string `yaml:"uid"`
	Node      string `yaml:"node"`
}

type nodeFixture struct {
	Cluster    string            `yaml:"cluster"`
	Node       string            `yaml:"node"`
	ExternalIP string            `yaml:"external_ip"`
	Labels     map[string]string `yaml:"labels"`
}

type pvcFixture struct {
	Cluster   string `yaml:"cluster"`
	Namespace string `yaml:"namespace"`
	Pod       string `yaml:"pod"`
	Volume    string `yaml:"volume"`
	Claim     string `yaml:"claim_name"`
}

type serviceGraphFixture struct {
	Client          string  `yaml:"client"`
	Server          string  `yaml:"server"`
	ClientCluster   string  `yaml:"client_cluster"`
	ServerCluster   string  `yaml:"server_cluster"`
	ClientPodUID    string  `yaml:"client_k8s_pod_uid"`
	ServerPodUID    string  `yaml:"server_k8s_pod_uid"`
	ClientNamespace string  `yaml:"client_k8s_namespace_name"`
	ServerNamespace string  `yaml:"server_k8s_namespace_name"`
	ConnectionType  string  `yaml:"connection_type"`
	Rate            float64 `yaml:"rate"`
}

type server struct {
	mu           sync.RWMutex
	current      *fixtureSet
	reloadTotal  atomic.Int64
	fixturesPath string
	logger       *slog.Logger
	// startedAt anchors the monotonic counter we synthesise for
	// traces_service_graph_request_total. PromQL rate() needs counter movement
	// across the scrape window; emitting `rate * elapsed_seconds` lets a real
	// rate(... [w]) query recover the configured per-second rate.
	startedAt time.Time
}

func main() {
	listen := flag.String("listen-addr", ":8080", "HTTP listen address")
	fixtures := flag.String("fixtures-file", "fixtures.yaml", "Path to YAML fixtures file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{}))
	slog.SetDefault(logger)

	s := &server{fixturesPath: *fixtures, logger: logger, startedAt: time.Now()}
	if err := s.reload(); err != nil {
		logger.Error("initial fixture load failed", "err", err)
		os.Exit(1)
	}

	go s.watchSIGHUP()

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/-/ready", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	logger.Info("vm-fixtures listening", "addr", *listen)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		logger.Error("http server failed", "err", err)
		os.Exit(1)
	}
}

func (s *server) watchSIGHUP() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	for range ch {
		if err := s.reload(); err != nil {
			s.logger.Error("reload failed", "err", err)
			continue
		}
		s.logger.Info("fixtures reloaded")
	}
}

func (s *server) reload() error {
	data, err := os.ReadFile(s.fixturesPath)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	var set fixtureSet
	if err := yaml.Unmarshal(data, &set); err != nil {
		return fmt.Errorf("yaml: %w", err)
	}
	s.mu.Lock()
	s.current = &set
	s.mu.Unlock()
	s.reloadTotal.Add(1)
	return nil
}

func (s *server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	set := s.current
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP vm_fixtures_reloaded_total Number of successful fixture reloads.\n")
	fmt.Fprintf(w, "# TYPE vm_fixtures_reloaded_total counter\n")
	fmt.Fprintf(w, "vm_fixtures_reloaded_total %d\n", s.reloadTotal.Load())

	for _, p := range set.Pods {
		fmt.Fprintf(w,
			`kube_pod_info{cluster=%q,namespace=%q,pod=%q,uid=%q,node=%q} 1`+"\n",
			p.Cluster, p.Namespace, p.Pod, p.UID, p.Node)
	}
	for _, n := range set.Nodes {
		fmt.Fprintf(w, `kube_node_info{cluster=%q,node=%q} 1`+"\n", n.Cluster, n.Node)
		if n.ExternalIP != "" {
			fmt.Fprintf(w,
				`kube_node_status_addresses{cluster=%q,node=%q,type="ExternalIP",address=%q} 1`+"\n",
				n.Cluster, n.Node, n.ExternalIP)
		}
		if len(n.Labels) > 0 {
			labels := make([]string, 0, len(n.Labels))
			labels = append(labels, fmt.Sprintf(`cluster=%q`, n.Cluster))
			labels = append(labels, fmt.Sprintf(`node=%q`, n.Node))
			keys := make([]string, 0, len(n.Labels))
			for k := range n.Labels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				labels = append(labels, fmt.Sprintf(`label_%s=%q`, escapeLabelName(k), n.Labels[k]))
			}
			fmt.Fprintf(w, `kube_node_labels{%s} 1`+"\n", joinComma(labels))
		}
	}
	for _, pv := range set.PVCs {
		fmt.Fprintf(w,
			`kube_pod_spec_volumes_persistentvolumeclaims_info{cluster=%q,namespace=%q,pod=%q,volume=%q,persistentvolumeclaim=%q} 1`+"\n",
			pv.Cluster, pv.Namespace, pv.Pod, pv.Volume, pv.Claim)
	}
	elapsed := time.Since(s.startedAt).Seconds()
	for _, c := range set.Calls {
		ct := c.ConnectionType
		if ct == "" {
			ct = "virtual_node"
		}
		counter := c.Rate * elapsed
		fmt.Fprintf(w,
			`traces_service_graph_request_total{client=%q,server=%q,client_cluster=%q,server_cluster=%q,client_k8s_pod_uid=%q,server_k8s_pod_uid=%q,client_k8s_namespace_name=%q,server_k8s_namespace_name=%q,connection_type=%q} %g`+"\n",
			c.Client, c.Server, c.ClientCluster, c.ServerCluster, c.ClientPodUID, c.ServerPodUID,
			c.ClientNamespace, c.ServerNamespace, ct, counter)
	}
}

func escapeLabelName(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '/' || c == '.' || c == '-':
			out[i] = '_'
		default:
			out[i] = c
		}
	}
	return string(out)
}

func joinComma(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "," + p
	}
	return out
}
