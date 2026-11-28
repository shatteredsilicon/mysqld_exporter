// Copyright 2018 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package collector

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus"
)

// Metric name parts.
const (
	// Subsystem(s).
	exporter = "exporter"
)

// SQL queries and parameters.
const (
	// System variable params formatting.
	// See: https://github.com/go-sql-driver/mysql#system-variables
	sessionSettingsParam = `log_slow_filter=%27tmp_table_on_disk,filesort_on_disk%27`
	timeoutParam         = `lock_wait_timeout=%d`
	maxStatementTime     = `max_statement_time=%f`
	maxExecutionTime     = `max_execution_time=%d`
)

var (
	defaultMaxStatementTime = 5 * time.Second
)

// Tunable flags.
var (
	_ = kingpin.Flag(
		"exporter.max-open-conns",
		"Maximum number of open connections to the database. https://golang.org/pkg/database/sql/#DB.SetMaxOpenConns",
	).Int()
	_ = kingpin.Flag(
		"exporter.max-idle-conns",
		"Maximum number of connections in the idle connection pool. https://golang.org/pkg/database/sql/#DB.SetMaxIdleConns",
	).Int()
	_ = kingpin.Flag(
		"exporter.conn-max-lifetime",
		"Maximum amount of time a connection may be reused. https://golang.org/pkg/database/sql/#DB.SetConnMaxLifetime",
	).Duration()
	excludeMonitoring = kingpin.Flag(
		"exporter.exclude_monitoring_from_slowlog",
		"Whether to exclude monitoring queries from slow log",
	).Envar("SSM_EXCLUDE_MONITORING_FROM_SLOWLOG").Bool()
	sqlCheckTimeout = kingpin.Flag(
		"exporter.sql_check_timeout",
		"# maximum time for running the quries, max_statement_time/max_execution_time for MariaDB/MySQL",
	).Envar("SSM_SQL_CHECK_TIMEOUT").Duration()
)

// metric definition
var (
	mysqlUp = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "up"),
		"Whether the MySQL server is up.",
		nil,
		nil,
	)
	mysqlScrapeCollectorSuccess = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, exporter, "collector_success"),
		"mysqld_exporter: Whether a collector succeeded.",
		[]string{"collector"},
		nil,
	)
	mysqlScrapeDurationSeconds = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, exporter, "collector_duration_seconds"),
		"Collector time duration.",
		[]string{"collector"}, nil,
	)
)

// Verify if Exporter implements prometheus.Collector
var _ prometheus.Collector = (*Exporter)(nil)

// Exporter collects MySQL metrics. It implements prometheus.Collector.
type Exporter struct {
	ctx      context.Context
	logger   *slog.Logger
	dsn      string
	scrapers []Scraper
	instance *instance

	enableLockWaitTimeout bool
	lockWaitTimeout       int
	slowLogFilter         bool
	queryTimeout          time.Duration
	maxOpenConns          int
}

type ExporterOpt func(*Exporter)

func EnableLockWaitTimeout(b bool) ExporterOpt {
	return func(e *Exporter) {
		e.enableLockWaitTimeout = b
	}
}

// SetLockWaitTimeout sets the lock_wait_timeout session variable in seconds.
func SetLockWaitTimeout(timeout int) ExporterOpt {
	return func(e *Exporter) {
		e.lockWaitTimeout = timeout
	}
}

func SetSlowLogFilter(b bool) ExporterOpt {
	return func(e *Exporter) {
		e.slowLogFilter = b
	}
}

// SetQueryTimeout sets a per-scraper query timeout. Zero disables the timeout
// and falls back to the parent (request) context.
func SetQueryTimeout(timeout time.Duration) ExporterOpt {
	return func(e *Exporter) {
		e.queryTimeout = timeout
	}
}

// SetMaxOpenConns sets the maximum number of open connections to the
// database used for each scrape.
func SetMaxOpenConns(n int) ExporterOpt {
	return func(e *Exporter) {
		e.maxOpenConns = n
	}
}

// withQueryTimeoutContext derives a context bounded by the configured query timeout.
// When the timeout is disabled (0), it returns the parent context and a no-op
// cancel so callers can unconditionally `defer cancel()`.
func (e *Exporter) withQueryTimeoutContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if e.queryTimeout > 0 {
		return context.WithTimeout(ctx, e.queryTimeout)
	}
	return ctx, func() {}
}

// New returns a new MySQL exporter for the provided DSN.
func New(ctx context.Context, dsn string, scrapers []Scraper, logger *slog.Logger, opts ...ExporterOpt) *Exporter {
	e := &Exporter{
		ctx:          ctx,
		logger:       logger,
		scrapers:     scrapers,
		maxOpenConns: 2,
	}

	for _, opt := range opts {
		opt(e)
	}

	// Setup extra params for the DSN
	dsnParams := []string{}

	// Only set lock_wait_timeout if it is enabled
	if e.enableLockWaitTimeout {
		dsnParams = append(dsnParams, fmt.Sprintf(timeoutParam, e.lockWaitTimeout))
	}

	if e.slowLogFilter {
		dsnParams = append(dsnParams, sessionSettingsParam)
	}

	if strings.Contains(dsn, "?") {
		dsn = dsn + "&"
	} else {
		dsn = dsn + "?"
	}
	dsn += strings.Join(dsnParams, "&")

	e.dsn = dsn

	return e
}

// Describe implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- mysqlUp
	ch <- mysqlScrapeDurationSeconds
	ch <- mysqlScrapeCollectorSuccess
}

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	up := e.scrape(e.ctx, ch)
	ch <- prometheus.MustNewConstMetric(mysqlUp, prometheus.GaugeValue, up)
}

// scrape collects metrics from the target, returns an up metric value.
func (e *Exporter) scrape(ctx context.Context, ch chan<- prometheus.Metric) float64 {
	var err error
	scrapeTime := time.Now()
	versionCtx, versionCancel := e.withQueryTimeoutContext(ctx)
	instance, err := newInstance(versionCtx, e.dsn, e.maxOpenConns)
	versionCancel()
	if err != nil {
		e.logger.Error("Error opening connection to database", "err", err)
		return 0.0
	}
	defer instance.Close()
	e.instance = instance

	pingCtx, pingCancel := e.withQueryTimeoutContext(ctx)
	defer pingCancel()
	if err := instance.Ping(pingCtx); err != nil {
		e.logger.Error("Error pinging mysqld", "err", err)
		return 0.0
	}

	ch <- prometheus.MustNewConstMetric(mysqlScrapeDurationSeconds, prometheus.GaugeValue, time.Since(scrapeTime).Seconds(), "connection")

	version := instance.versionMajorMinor

	var wg sync.WaitGroup
	defer wg.Wait()
	for _, scraper := range e.scrapers {
		if version < scraper.Version() {
			continue
		}

		wg.Go(func() {
			label := "collect." + scraper.Name()
			scrapeTime := time.Now()
			collectorSuccess := 1.0
			scrapeCtx, cancel := e.withQueryTimeoutContext(ctx)
			defer cancel()
			if err := scraper.Scrape(scrapeCtx, instance, ch, e.logger.With("scraper", scraper.Name())); err != nil {
				e.logger.Error("Error from scraper", "scraper", scraper.Name(), "target", e.getTargetFromDsn(), "err", err)
				collectorSuccess = 0.0
			}
			ch <- prometheus.MustNewConstMetric(mysqlScrapeCollectorSuccess, prometheus.GaugeValue, collectorSuccess, label)
			ch <- prometheus.MustNewConstMetric(mysqlScrapeDurationSeconds, prometheus.GaugeValue, time.Since(scrapeTime).Seconds(), label)
		})
	}
	return 1.0
}

func (e *Exporter) getTargetFromDsn() string {
	// Get target from DSN.
	dsnConfig, err := mysql.ParseDSN(e.dsn)
	if err != nil {
		e.logger.Error("Error parsing DSN", "err", err)
		return ""
	}
	return dsnConfig.Addr
}
