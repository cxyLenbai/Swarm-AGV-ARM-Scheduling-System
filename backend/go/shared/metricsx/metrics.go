package metricsx

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests.",
		},
		[]string{"method", "path", "status"},
	)
	httpLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path", "status"},
	)
	kafkaConsumerLag = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kafka_consumer_lag",
			Help: "Kafka consumer lag by topic.",
		},
		[]string{"topic", "group"},
	)
	influxWriteFailures = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "influx_write_failures_total",
			Help: "Total InfluxDB write failures.",
		},
	)
	aiPredictFailures = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "ai_predict_failures_total",
			Help: "Total AI prediction failures.",
		},
	)
	aiPredictSuccess = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "ai_predict_success_total",
			Help: "Total AI prediction successes.",
		},
	)
	aiPredictLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "ai_predict_latency_seconds",
			Help:    "AI prediction latency in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)
	schedulerDecisionLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "scheduler_decision_latency_seconds",
			Help:    "Scheduler decision processing latency in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)
	asynqQueueDepth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "asynq_queue_depth",
			Help: "Asynq queue depth by queue.",
		},
		[]string{"queue"},
	)
)

func Register() {
	prometheus.MustRegister(httpRequests, httpLatency, kafkaConsumerLag, influxWriteFailures, aiPredictFailures, aiPredictSuccess, aiPredictLatency, schedulerDecisionLatency, asynqQueueDepth)
}

func Handler() http.Handler {
	return promhttp.Handler()
}

func Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(lrw, r)
		status := strconv.Itoa(lrw.statusCode)
		httpRequests.WithLabelValues(r.Method, r.URL.Path, status).Inc()
		httpLatency.WithLabelValues(r.Method, r.URL.Path, status).Observe(time.Since(start).Seconds())
	})
}

func SetKafkaLag(topic string, group string, lag int64) {
	kafkaConsumerLag.WithLabelValues(topic, group).Set(float64(lag))
}

func IncInfluxWriteFailure() {
	influxWriteFailures.Inc()
}

func IncAIPredictFailure() {
	aiPredictFailures.Inc()
}

func IncAIPredictSuccess() {
	aiPredictSuccess.Inc()
}

func ObserveAIPredictLatency(d time.Duration) {
	aiPredictLatency.Observe(d.Seconds())
}

func ObserveSchedulerDecisionLatency(d time.Duration) {
	schedulerDecisionLatency.Observe(d.Seconds())
}

func SetAsynqQueueDepth(queue string, depth int) {
	asynqQueueDepth.WithLabelValues(queue).Set(float64(depth))
}

type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}
