package metrics

import (
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	DeploymentsActive *prometheus.GaugeVec
	DeploymentsTotal  *prometheus.CounterVec
	DeploymentsFailed *prometheus.CounterVec
	RequestDuration   *prometheus.HistogramVec
)

func Init(subsystem string) {
	DeploymentsActive = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "margo",
			Subsystem: subsystem,
			Name:      "deployments_active",
			Help:      fmt.Sprintf("Active deployments in %s", subsystem),
		},
		[]string{"site"},
	)

	DeploymentsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "margo",
			Subsystem: subsystem,
			Name:      "deployments_total",
			Help:      fmt.Sprintf("Total deployments handled by %s", subsystem),
		},
		[]string{"site"},
	)

	DeploymentsFailed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "margo",
			Subsystem: subsystem,
			Name:      "deployments_failed_total",
			Help:      fmt.Sprintf("Failed deployments in %s", subsystem),
		},
		[]string{"site"},
	)

	RequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "margo",
			Subsystem: subsystem,
			Name:      "request_duration_seconds",
			Help:      fmt.Sprintf("Request duration in %s", subsystem),
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"endpoint"},
	)

	prometheus.MustRegister(DeploymentsActive, DeploymentsTotal, DeploymentsFailed, RequestDuration)
}

func StartServer(port string) {
	http.Handle("/metrics", promhttp.Handler())
	go func() {
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			panic("metrics server failed: " + err.Error())
		}
	}()
}
