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

package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"path"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/shatteredsilicon/mysqld_exporter/collector"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/proxy"
	"gopkg.in/ini.v1"
	"gopkg.in/yaml.v2"

	mysqldConfig "github.com/shatteredsilicon/mysqld_exporter/config"
)

const webAuthFileFlagName = "web.auth-file"

var (
	metricsPath = kingpin.Flag(
		"web.telemetry-path",
		"Path under which to expose metrics.",
	).Default("/metrics").String()
	listenAddress = kingpin.Flag(
		"web.listen-address",
		"Address on which to expose metrics and web interface.",
	).Strings()
	webAuthFile = kingpin.Flag(
		webAuthFileFlagName,
		"Path to YAML file with server_user, server_password keys for HTTP Basic authentication.",
	).String()
	webConfigFile = kingpin.Flag(
		"web.config.file",
		"Path to prometheus web config file (YAML).",
	).String()
	systemdSocket = kingpin.Flag(
		"web.systemd-socket",
		"Use systemd socket activation listeners instead of port listeners (Linux only).",
	).Bool()
	sslCertFile = kingpin.Flag(
		"web.ssl-cert-file",
		"Path to SSL certificate file.",
	).String()
	sslKeyFile = kingpin.Flag(
		"web.ssl-key-file",
		"Path to SSL key file.",
	).String()
	timeoutOffset = kingpin.Flag(
		"timeout-offset",
		"Offset to subtract from timeout in seconds.",
	).Default("0.25").Float64()
	configMycnf = kingpin.Flag(
		"config.my-cnf",
		"Path to .my.cnf file to read MySQL credentials from.",
	).Default(".my.cnf").String()
	mysqldAddress = kingpin.Flag(
		"mysqld.address",
		"Address to use for connecting to MySQL",
	).Default("localhost:3306").String()
	mysqldUser = kingpin.Flag(
		"mysqld.username",
		"Username to use for connecting to MySQL",
	).String()
	tlsInsecureSkipVerify = kingpin.Flag(
		"tls.insecure-skip-verify",
		"Ignore certificate and server verification when using a tls connection.",
	).Bool()
	exporterGlobalConnPool = kingpin.Flag(
		"exporter.global-conn-pool",
		"Use global connection pool instead of creating new pool for each http request.",
	).Bool()
	collectAll = kingpin.Flag(
		"collect.all",
		"Collect all metrics.",
	).Bool()
	configPath = kingpin.Flag(
		"config",
		"Path of config file",
	).Default("/opt/ss/ssm-client/mysqld_exporter.conf").String()
	promlogConfig = &promlog.Config{
		Level:  &promlog.AllowedLevel{},
		Format: &promlog.AllowedFormat{},
	}

	_ = kingpin.Flag("c", "").Hidden().Short('c').Action(convertFlagAction('c')).Strings()
	_ = kingpin.Flag("w", "").Hidden().Short('w').Action(convertFlagAction('w')).Strings()
	_ = kingpin.Flag("e", "").Hidden().Short('e').Action(convertFlagAction('e')).Strings()
	_ = kingpin.Flag("t", "").Hidden().Short('t').Action(convertFlagAction('t')).Strings()

	c = mysqldConfig.MySqlConfigHandler{
		Config: &mysqldConfig.Config{},
	}
	db *sql.DB
)

// scrapers lists all possible collection methods and if they should be enabled by default.
var scrapers = map[collector.Scraper]bool{
	collector.ScrapeGlobalStatus{}:                        true,
	collector.ScrapeGlobalVariables{}:                     true,
	collector.ScrapeSlaveStatus{}:                         true,
	collector.ScrapeProcesslist{}:                         false,
	collector.ScrapeUser{}:                                false,
	collector.ScrapeTableSchema{}:                         false,
	collector.ScrapeInfoSchemaInnodbTablespaces{}:         false,
	collector.ScrapeInnodbMetrics{}:                       false,
	collector.ScrapeAutoIncrementColumns{}:                false,
	collector.ScrapeBinlogSize{}:                          false,
	collector.ScrapePerfTableIOWaits{}:                    false,
	collector.ScrapePerfIndexIOWaits{}:                    false,
	collector.ScrapePerfTableLockWaits{}:                  false,
	collector.ScrapePerfEventsStatements{}:                false,
	collector.ScrapePerfEventsStatementsSum{}:             false,
	collector.ScrapePerfEventsWaits{}:                     false,
	collector.ScrapePerfFileEvents{}:                      false,
	collector.ScrapePerfFileInstances{}:                   false,
	collector.ScrapePerfMemoryEvents{}:                    false,
	collector.ScrapePerfReplicationGroupMembers{}:         false,
	collector.ScrapePerfReplicationGroupMemberStats{}:     false,
	collector.ScrapePerfReplicationApplierStatsByWorker{}: false,
	collector.ScrapeSysUserSummary{}:                      false,
	collector.ScrapeUserStat{}:                            false,
	collector.ScrapeClientStat{}:                          false,
	collector.ScrapeTableStat{}:                           false,
	collector.ScrapeSchemaStat{}:                          false,
	collector.ScrapeInnodbCmp{}:                           true,
	collector.ScrapeInnodbCmpMem{}:                        true,
	collector.ScrapeQueryResponseTime{}:                   true,
	collector.ScrapeEngineTokudbStatus{}:                  false,
	collector.ScrapeEngineInnodbStatus{}:                  false,
	collector.ScrapeHeartbeat{}:                           false,
	collector.ScrapeSlaveHosts{}:                          false,
	collector.ScrapeReplicaHost{}:                         false,
	collector.ScrapeCustomQuery{}:                         true,
	collector.ScrapeIndexStat{}:                           false,
}

var scrapersHr = map[collector.Scraper]struct{}{
	collector.ScrapeGlobalStatus{}:  {},
	collector.ScrapeInnodbMetrics{}: {},
}

var scrapersMr = map[collector.Scraper]struct{}{
	collector.ScrapeSlaveStatus{}:        {},
	collector.ScrapeProcesslist{}:        {},
	collector.ScrapePerfEventsWaits{}:    {},
	collector.ScrapePerfFileEvents{}:     {},
	collector.ScrapePerfTableLockWaits{}: {},
	collector.ScrapeQueryResponseTime{}:  {},
	collector.ScrapeEngineInnodbStatus{}: {},
	collector.ScrapeInnodbCmp{}:          {},
	collector.ScrapeInnodbCmpMem{}:       {},
}

var scrapersLr = map[collector.Scraper]struct{}{
	collector.ScrapeGlobalVariables{}:             {},
	collector.ScrapeTableSchema{}:                 {},
	collector.ScrapeAutoIncrementColumns{}:        {},
	collector.ScrapeBinlogSize{}:                  {},
	collector.ScrapePerfTableIOWaits{}:            {},
	collector.ScrapePerfIndexIOWaits{}:            {},
	collector.ScrapePerfFileInstances{}:           {},
	collector.ScrapeUserStat{}:                    {},
	collector.ScrapeTableStat{}:                   {},
	collector.ScrapeIndexStat{}:                   {},
	collector.ScrapePerfEventsStatements{}:        {},
	collector.ScrapeClientStat{}:                  {},
	collector.ScrapeInfoSchemaInnodbTablespaces{}: {},
	collector.ScrapeEngineTokudbStatus{}:          {},
	collector.ScrapeHeartbeat{}:                   {},
	collector.ScrapeCustomQuery{}:                 {},
}

func filterScrapers(scrapers []collector.Scraper, collectParams []string) []collector.Scraper {
	var filteredScrapers []collector.Scraper

	// Check if we have some "collect[]" query parameters.
	if len(collectParams) > 0 {
		filters := make(map[string]bool)
		for _, param := range collectParams {
			filters[param] = true
		}

		for _, scraper := range scrapers {
			if filters[scraper.Name()] {
				filteredScrapers = append(filteredScrapers, scraper)
			}
		}
	}
	if len(filteredScrapers) == 0 {
		return scrapers
	}
	return filteredScrapers
}

func init() {
	proxyDialer := proxy.FromEnvironment()
	directDialer := proxy.Direct
	mysqlDriver.RegisterDialContext("tcp", func(ctx context.Context, addr string) (net.Conn, error) {
		ip, _ := net.ResolveTCPAddr("tcp", addr)
		iAddrs, _ := net.InterfaceAddrs()
		if ip == nil || len(iAddrs) == 0 {
			return proxyDialer.Dial("tcp", addr)
		}

		for _, iAddr := range iAddrs {
			if ipNet, ok := iAddr.(*net.IPNet); ok && ipNet.IP.Equal(ip.IP) {
				return directDialer.Dial("tcp", addr)
			}
		}

		return proxyDialer.Dial("tcp", addr)
	})
}

func newHandler(db *sql.DB, scrapers []collector.Scraper, logger log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var dsn string
		var err error
		target := ""
		q := r.URL.Query()
		if q.Has("target") {
			target = q.Get("target")
		}

		cfg := c.GetConfig()
		cfgsection, ok := cfg.Sections["client"]
		if !ok {
			level.Error(logger).Log("msg", "Failed to parse section [client] from config file", "err", err)
		}
		if dsn, err = cfgsection.FormDSN(target); err != nil {
			level.Error(logger).Log("msg", "Failed to form dsn from section [client]", "err", err)
		}

		collect := q["collect[]"]

		// Use request context for cancellation when connection gets closed.
		ctx := r.Context()
		// If a timeout is configured via the Prometheus header, add it to the context.
		if v := r.Header.Get("X-Prometheus-Scrape-Timeout-Seconds"); v != "" {
			timeoutSeconds, err := strconv.ParseFloat(v, 64)
			if err != nil {
				level.Error(logger).Log("msg", "Failed to parse timeout from Prometheus header", "err", err)
			} else {
				if *timeoutOffset >= timeoutSeconds {
					// Ignore timeout offset if it doesn't leave time to scrape.
					level.Error(logger).Log("msg", "Timeout offset should be lower than prometheus scrape timeout", "offset", *timeoutOffset, "prometheus_scrape_timeout", timeoutSeconds)
				} else {
					// Subtract timeout offset from timeout.
					timeoutSeconds -= *timeoutOffset
				}
				// Create new timeout context with request context as parent.
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSeconds*float64(time.Second)))
				defer cancel()
				// Overwrite request with timeout context.
				r = r.WithContext(ctx)
			}
		}

		filteredScrapers := filterScrapers(scrapers, collect)

		registry := prometheus.NewRegistry()

		db := db
		if target != "" {
			db = nil
		}
		registry.MustRegister(collector.New(ctx, db, dsn, target, filteredScrapers, logger))

		gatherers := prometheus.Gatherers{
			prometheus.DefaultGatherer,
			registry,
		}
		// Delegate http serving to Prometheus client library, which will call collector.Collect.
		h := promhttp.HandlerFor(gatherers, promhttp.HandlerOpts{})
		h.ServeHTTP(w, r)
	}
}

func reloadMySqlConfig(logger log.Logger) error {
	if cfg.Exporter.DSN != "" { // DSN has higher priority
		if err := c.ReloadConfigFromDSN(cfg.Exporter.DSN, logger); err != nil {
			return err
		}
	} else {
		if err := c.ReloadConfig(*configMycnf, *mysqldAddress, *mysqldUser, *tlsInsecureSkipVerify, logger); err != nil {
			return err
		}
	}
	if *exporterGlobalConnPool {
		dsn, err := c.GetConfig().Sections["client"].FormDSN("")
		if err != nil {
			return err
		}

		newDB, err := collector.NewDB(dsn, "")
		if err != nil {
			return err
		}

		if db != nil {
			if err = db.Close(); err != nil {
				newDB.Close()
				return err
			}
		}
		db = newDB
	}

	return nil
}

var cfg = new(config)
var setByUserMap = make(map[string]bool)

func setByUserFlagAction() func(ctx *kingpin.ParseContext) error {
	executed := false

	return func(pc *kingpin.ParseContext) error {
		if executed {
			return nil
		}

		for _, elem := range pc.Elements {
			if elem.Clause == nil {
				continue
			}

			flagClause, ok := elem.Clause.(*kingpin.FlagClause)
			if !ok || flagClause == nil {
				continue
			}

			setByUserMap[flagClause.Model().Name] = true
		}

		executed = true
		return nil
	}
}

// this function is for translating single-hyphen flags into long flags,
// to make it compatible with earily PMM/SSM version of node_exporter
func convertFlagAction(short rune) func(ctx *kingpin.ParseContext) error {
	convertedMap := make(map[rune]bool)

	return func(pc *kingpin.ParseContext) error {
		if convertedMap[short] {
			return nil
		}

		for _, elem := range pc.Elements {
			if elem.Clause == nil {
				continue
			}

			flagClause, ok := elem.Clause.(*kingpin.FlagClause)
			if !ok || flagClause.Model().Short != short {
				continue
			}

			ctx, err := kingpin.CommandLine.ParseContext([]string{fmt.Sprintf("--%c%s", short, *elem.Value)})
			if err != nil && ctx != nil && len(ctx.Elements) > 0 && ctx.Elements[0].Clause != nil {
				// with standard flag package, single-hyphen bool flag is in format
				// '-<name>=<bool>', this code block here tries to translate it into
				// kingpin long bool flag

				clause, ok := ctx.Elements[0].Clause.(*kingpin.FlagClause)
				if !ok || !clause.Model().IsBoolFlag() {
					return err
				}

				boolStrs := strings.Split(*elem.Value, "=")
				if len(boolStrs) == 1 {
					return err
				}

				var boolValue bool
				boolValue, err = strconv.ParseBool(boolStrs[len(boolStrs)-1])
				if err != nil {
					return err
				}

				if boolValue {
					ctx, err = kingpin.CommandLine.ParseContext([]string{fmt.Sprintf("--%s", clause.Model().Name)})
				} else {
					ctx, err = kingpin.CommandLine.ParseContext([]string{fmt.Sprintf("--no-%s", clause.Model().Name)})
				}
			}
			if err != nil || ctx == nil || len(ctx.Elements) == 0 || ctx.Elements[0].Clause == nil {
				return err
			}

			flag, ok := ctx.Elements[0].Clause.(*kingpin.FlagClause)
			if !ok {
				return fmt.Errorf("unknow flag")
			}

			setByUserMap[flag.Model().Name] = true
			if err = flag.Model().Value.Set(*ctx.Elements[0].Value); err != nil {
				return err
			}
		}

		convertedMap[short] = true
		return nil
	}
}

func init() {
	kingpin.Flag(flag.LevelFlagName, flag.LevelFlagHelp).
		Default("info").SetValue(promlogConfig.Level)
	kingpin.Flag(flag.FormatFlagName, flag.FormatFlagHelp).
		Default("logfmt").SetValue(promlogConfig.Format)

	kingpin.CommandLine.PreAction(setByUserFlagAction())
}

func main() {
	defer func() {
		if db != nil {
			db.Close()
		}
	}()

	// Generate ON/OFF flags for all scrapers.
	scraperFlags := map[collector.Scraper]*bool{}
	for scraper, enabledByDefault := range scrapers {
		defaultOn := "false"
		if enabledByDefault {
			defaultOn = "true"
		}

		f := kingpin.Flag(
			"collect."+scraper.Name(),
			scraper.Help(),
		).Default(defaultOn).Bool()

		scraperFlags[scraper] = f
	}

	// Parse flags.
	kingpin.Version(version.Print("mysqld_exporter"))
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promlog.New(promlogConfig)

	if err := ini.MapTo(&cfg, *configPath); err != nil {
		stdlog.Fatalf(fmt.Sprintf("Load config file %s failed: %s\n", *configPath, err.Error()))
	}

	if cfg.Config.MyCnf == "" {
		cfg.Config.MyCnf = path.Join(os.Getenv("HOME"), ".my.cnf")
	}
	if dsn := os.Getenv("DATA_SOURCE_NAME"); dsn != "" {
		cfg.Exporter.DSN = dsn
	}

	if os.Getenv("ON_CONFIGURE") == "1" {
		err := configure()
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	// override flag value with config value
	// if it's not set
	overrideFlags()

	level.Info(logger).Log("msg", "Starting mysqld_exporter", "version", version.Info())
	level.Info(logger).Log("msg", "Build context", "build_context", version.BuildContext())

	var err error
	if err = reloadMySqlConfig(logger); err != nil {
		level.Info(logger).Log("msg", "Error parsing host config", "file", *configMycnf, "err", err)
		os.Exit(1)
	}

	// Register only scrapers enabled by flag.
	enabledScrapers := []collector.Scraper{}
	enabledHrScrapers := []collector.Scraper{}
	enabledMrScrapers := []collector.Scraper{}
	enabledLrScrapers := []collector.Scraper{}
	for scraper, enabled := range scraperFlags {
		if *enabled || *collectAll {
			level.Info(logger).Log("msg", "Scraper enabled", "scraper", scraper.Name())
			enabledScrapers = append(enabledScrapers, scraper)
			if _, ok := scrapersHr[scraper]; ok {
				enabledHrScrapers = append(enabledHrScrapers, scraper)
			}
			if _, ok := scrapersMr[scraper]; ok {
				enabledMrScrapers = append(enabledMrScrapers, scraper)
			}
			if _, ok := scrapersLr[scraper]; ok {
				enabledLrScrapers = append(enabledLrScrapers, scraper)
			}
		}
	}

	handlerFunc := newHandler(db, enabledScrapers, logger)
	http.Handle(*metricsPath, promhttp.InstrumentMetricHandler(prometheus.DefaultRegisterer, handlerFunc))
	var extraPathPrefix string
	if *metricsPath != "/" && *metricsPath != "" {
		landingConfig := web.LandingConfig{
			Name:        "MySQLd Exporter",
			Description: "Prometheus Exporter for MySQL servers",
			Version:     version.Info(),
			Links: []web.LandingLinks{
				{
					Address: *metricsPath,
					Text:    "Metrics",
				},
			},
		}
		landingPage, err := web.NewLandingPage(landingConfig)
		if err != nil {
			level.Error(logger).Log("err", err)
			os.Exit(1)
		}
		http.Handle("/", landingPage)

		extraPathPrefix = *metricsPath + "-"
	}

	hrHandlerFunc := newHandler(db, enabledHrScrapers, logger)
	http.Handle(extraPathPrefix+"hr", promhttp.InstrumentMetricHandler(prometheus.DefaultRegisterer, hrHandlerFunc))
	mrHandlerFunc := newHandler(db, enabledMrScrapers, logger)
	http.Handle(extraPathPrefix+"mr", promhttp.InstrumentMetricHandler(prometheus.DefaultRegisterer, mrHandlerFunc))
	lrHandlerFunc := newHandler(db, enabledLrScrapers, logger)
	http.Handle(extraPathPrefix+"lr", promhttp.InstrumentMetricHandler(prometheus.DefaultRegisterer, lrHandlerFunc))

	http.HandleFunc("/probe", handleProbe(db, enabledScrapers, logger))
	http.HandleFunc("/-/reload", func(w http.ResponseWriter, r *http.Request) {
		if err = reloadMySqlConfig(logger); err != nil {
			level.Warn(logger).Log("msg", "Error reloading host config", "file", *configMycnf, "error", err)
			return
		}
		_, _ = w.Write([]byte(`ok`))
	})

	authConfigBytes, err := os.ReadFile(*webAuthFile)
	if err != nil {
		level.Error(logger).Log("err", err)
		os.Exit(1)
	}
	var authC authConfig
	if err := yaml.Unmarshal(authConfigBytes, &authC); err != nil {
		level.Error(logger).Log("err", err)
		os.Exit(1)
	}

	tlsMinVer := (web.TLSVersion)(tls.VersionTLS10)
	tlsMaxVer := (web.TLSVersion)(tls.VersionTLS13)

	prometheusWebConfig := prometheusWebConfig{
		TLSConfig: prometheusTLSConfig{
			MinVersion: &tlsMinVer,
			MaxVersion: &tlsMaxVer,
		},
	}
	if authC.ServerUser != "" {
		hashedPsw, err := bcrypt.GenerateFromPassword([]byte(authC.ServerPassword), 0)
		if err != nil {
			level.Error(logger).Log("err", err)
			os.Exit(1)
		}
		prometheusWebConfig.Users = map[string]string{
			authC.ServerUser: string(hashedPsw),
		}
	}
	if *sslCertFile != "" || *sslKeyFile != "" {
		prometheusWebConfig.TLSConfig.TLSCertPath = *sslCertFile
		prometheusWebConfig.TLSConfig.TLSKeyPath = *sslKeyFile
	}

	if *webConfigFile == "" {
		level.Error(logger).Log("Use web.config.file flag/config to tell the location of prometheus web file")
		os.Exit(1)
	}
	webConfigBytes, err := yaml.Marshal(prometheusWebConfig)
	if err != nil {
		level.Error(logger).Log("err", err)
		os.Exit(1)
	}
	if err = os.WriteFile(*webConfigFile, webConfigBytes, 0600); err != nil {
		level.Error(logger).Log("err", err)
		os.Exit(1)
	}

	srv := &http.Server{}
	toolkitFlags := &web.FlagConfig{
		WebSystemdSocket:   systemdSocket,
		WebListenAddresses: listenAddress,
		WebConfigFile:      webConfigFile,
	}
	if err := web.ListenAndServe(srv, toolkitFlags, logger); err != nil {
		level.Error(logger).Log("msg", "Error starting HTTP server", "err", err)
		os.Exit(1)
	}
}

type config struct {
	TimeoutOffset float64        `ini:"timeout-offset"`
	Config        configConfig   `ini:"config"`
	Collect       collectConfig  `ini:"collect"`
	Web           webConfig      `ini:"web"`
	Exporter      exporterConfig `ini:"exporter"`
	TLS           tlsConfig      `ini:"tls"`
	MySQLD        mysqlD         `ini:"mysqld"`
}

type collectConfig struct {
	All                  bool `ini:"all"`
	GlobalStatus         bool `ini:"global_status"`
	GlobalVariables      bool `ini:"global_variables"`
	SlaveStatus          bool `ini:"slave_status"`
	ProcessList          bool `ini:"info_schema.processlist"`
	TableSchema          bool `ini:"info_schema.tables"`
	InnodbTableSpaces    bool `ini:"info_schema.innodb_tablespaces"`
	InnodbMetrics        bool `ini:"info_schema.innodb_metrics"`
	AutoIncrementColumns bool `ini:"auto_increment.columns"`
	BinlogSize           bool `ini:"binlog_size"`
	PerfTableIOWaits     bool `ini:"perf_schema.tableiowaits"`
	PerfIndexIOWaits     bool `ini:"perf_schema.indexiowaits"`
	PerfTableLockWaits   bool `ini:"perf_schema.tablelocks"`
	PerfEventsStatements bool `ini:"perf_schema.eventsstatements"`
	PerfEventsWaits      bool `ini:"perf_schema.eventswaits"`
	PerfFileEvents       bool `ini:"perf_schema.file_events"`
	PerfFileInstances    bool `ini:"perf_schema.file_instances"`
	UserStat             bool `ini:"info_schema.userstats"`
	ClientStat           bool `ini:"info_schema.clientstats"`
	TableStat            bool `ini:"info_schema.tablestats"`
	IndexStat            bool `ini:"info_schema.indexstats"`
	QueryResponseTime    bool `ini:"info_schema.query_response_time"`
	EngineTokudbStatus   bool `ini:"engine_tokudb_status"`
	EngineInnodbStatus   bool `ini:"engine_innodb_status"`
	Heartbeat            bool `ini:"heartbeat"`
	InnodbCmp            bool `ini:"info_schema.innodb_cmp"`
	InnodbCmpMem         bool `ini:"info_schema.innodb_cmpmem"`
	CustomQuery          bool `ini:"custom_query"`

	collector.HeartbeatConfig              `ini:"collect"`
	collector.InfoSchemaProcessListConfig  `ini:"collect"`
	collector.InfoSchemaTablesConfig       `ini:"collect"`
	collector.MySQLUserConfig              `ini:"collect"`
	collector.PerfSchemaFileInstConfig     `ini:"collect"`
	collector.PerfSchemaMemoryEventsConfig `ini:"collect"`
}

type webConfig struct {
	ListenAddress string `ini:"listen-address"`
	TelemetryPath string `ini:"telemetry-path"`
	AuthFile      string `ini:"auth-file"`
	ConfigFile    string `ini:"config.file"`
	SSLCertFile   string `ini:"ssl-cert-file"`
	SSLKeyFile    string `ini:"ssl-key-file"`
	SystemdSocket bool   `ini:"systemd-socket"`
}

type exporterConfig struct {
	LockWaitTimeout int    `ini:"lock_wait_timeout"`
	LogSlowFilter   bool   `ini:"log_slow_filter"`
	GlobalConnPool  bool   `ini:"global-conn-pool"`
	MaxOpenConns    int    `ini:"max-open-conns"`
	MaxIdleConns    int    `ini:"max-idle-conns"`
	ConnMaxLifetime string `ini:"conn-max-lifetime"`
	DSN             string `ini:"dsn"`
}

type configConfig struct {
	MyCnf string `ini:"my-cnf"`
}

type tlsConfig struct {
	InsecureSkipVerify bool `ini:"insecure-skip-verify"`
}

type mysqlD struct {
	Address  string `ini:"address"`
	Username string `ini:"username"`
}

func configVisit(visitFn func(string, string, reflect.Value)) {
	type item struct {
		value   reflect.Value
		section string
	}

	items := []item{
		{
			value:   reflect.ValueOf(cfg).Elem(),
			section: "",
		},
	}
	for i := 0; i < len(items); i++ {
		for j := 0; j < items[i].value.Type().NumField(); j++ {
			fieldValue := items[i].value.Field(j)
			fieldType := items[i].value.Type().Field(j)
			section := items[i].section
			key := strings.SplitN(fieldType.Tag.Get("ini"), ",", 2)[0]

			if fieldValue.Kind() == reflect.Struct {
				if fieldValue.CanAddr() {
					if section == "" {
						section = key
					} else if section != key {
						section = fmt.Sprintf("%s.%s", section, key)
					}

					items = append(items, item{
						value:   fieldValue.Addr().Elem(),
						section: section,
					})
				}
				continue
			}

			visitFn(section, key, fieldValue)
		}
	}
}

func configure() error {
	iniCfg, err := ini.Load(*configPath)
	if err != nil {
		return err
	}

	if err = iniCfg.MapTo(cfg); err != nil {
		return err
	}

	configVisit(func(section, key string, fieldValue reflect.Value) {
		flagKey := fmt.Sprintf("%s.%s", section, key)
		if section == "" {
			flagKey = key
		}

		setByUser := setByUserMap[flagKey]
		kingpinF := kingpin.CommandLine.GetFlag(flagKey)
		if !setByUser || kingpinF == nil {
			return
		}

		// Don't override web.auth-file config
		if flagKey == webAuthFileFlagName {
			return
		}

		iniCfg.Section(section).Key(key).SetValue(kingpinF.Model().Value.String())
	})

	if dsn := os.Getenv("DATA_SOURCE_NAME"); dsn != "" {
		iniCfg.Section("exporter").Key("dsn").SetValue(strconv.Quote(dsn))
	}

	if err = iniCfg.SaveTo(*configPath); err != nil {
		return err
	}

	return nil
}

func overrideFlags() {
	configVisit(func(section, key string, fieldValue reflect.Value) {
		flagKey := fmt.Sprintf("%s.%s", section, key)
		if section == "" {
			flagKey = key
		}

		setByUser := setByUserMap[flagKey]
		kingpinF := kingpin.CommandLine.GetFlag(flagKey)
		if setByUser || kingpinF == nil {
			return
		}

		var values []reflect.Value
		if fieldValue.Kind() == reflect.Slice {
			for i := 0; i < fieldValue.Len(); i++ {
				values = append(values, fieldValue.Index(i))
			}
		} else {
			values = []reflect.Value{fieldValue}
		}

		for i := range values {
			switch values[i].Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Float32, reflect.Int64:
				kingpinF.Model().Value.Set(strconv.FormatInt(values[i].Int(), 10))
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				kingpinF.Model().Value.Set(strconv.FormatUint(values[i].Uint(), 10))
			case reflect.Bool:
				kingpinF.Model().Value.Set(strconv.FormatBool(values[i].Bool()))
			default:
				kingpinF.Model().Value.Set(values[i].String())
			}
		}
	})
}

type authConfig struct {
	ServerUser     string `yaml:"server_user,omitempty"`
	ServerPassword string `yaml:"server_password,omitempty"`
}

type prometheusWebConfig struct {
	TLSConfig prometheusTLSConfig `yaml:"tls_server_config"`
	Users     map[string]string   `yaml:"basic_auth_users"`
}

type prometheusTLSConfig struct {
	TLSCertPath string          `yaml:"cert_file"`
	TLSKeyPath  string          `yaml:"key_file"`
	MinVersion  *web.TLSVersion `yaml:"min_version"`
	MaxVersion  *web.TLSVersion `yaml:"max_version"`
}
