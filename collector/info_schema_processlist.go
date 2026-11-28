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

// Scrape `information_schema.processlist`.

package collector

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/shatteredsilicon/mysqld_exporter/config"
)

const infoSchemaProcesslistQuery = `
		  SELECT
		    user,
		    SUBSTRING_INDEX(host, ':', 1) AS host,
		    COALESCE(command, '') AS command,
		    COALESCE(state, '') AS state,
		    COUNT(*) AS processes,
		    SUM(time) AS seconds%s
		  FROM information_schema.processlist
		  WHERE ID != connection_id()
		    AND TIME >= %d
		  GROUP BY user, host, command, state
	`

const infoSchemaProcesslistColumnsQuery = `
	SELECT column_name
	FROM information_schema.columns
	WHERE table_schema = 'information_schema' AND table_name = 'processlist'
`

// Metric descriptors.
var (
	processlistCountDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, informationSchema, "processlist_threads"),
		"The number of threads split by current state.",
		[]string{"command", "state"}, nil)
	processlistTimeDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, informationSchema, "processlist_seconds"),
		"The number of seconds threads have used split by current state.",
		[]string{"command", "state"}, nil)
	processesByUserDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, informationSchema, "processlist_processes_by_user"),
		"The number of processes by user.",
		[]string{"mysql_user"}, nil)
	processesByHostDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, informationSchema, "processlist_processes_by_host"),
		"The number of processes by host.",
		[]string{"client_host"}, nil)
	processlistMemoryUsedDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, informationSchema, "processlist_memory_used"),
		"The size of used memory shown in table information_schema.processlist.",
		[]string{}, nil,
	)
)

var (
	plMemoryUsedExists *bool
	plMemoryUsedSelect string
)

// ScrapeProcesslist collects from `information_schema.processlist`.
type ScrapeProcesslist config.InfoSchemaProcesslistConfig

// Name of the Scraper. Should be unique.
func (ScrapeProcesslist) Name() string {
	return informationSchema + ".processlist"
}

// Help describes the role of the Scraper.
func (ScrapeProcesslist) Help() string {
	return "Collect current thread state counts from the information_schema.processlist"
}

// Version of MySQL from which scraper is available.
func (ScrapeProcesslist) Version() float64 {
	return 5.1
}

func (s ScrapeProcesslist) Scrape(ctx context.Context, instance *instance, ch chan<- prometheus.Metric, logger *slog.Logger) error {
	// check if column 'memory_used' exists
	checkPLMemoryUsedColumn(ctx, instance.db, logger)

	processQuery := fmt.Sprintf(
		infoSchemaProcesslistQuery,
		plMemoryUsedSelect,
		s.MinTime,
	)
	db := instance.getDB()
	processlistRows, err := db.QueryContext(ctx, processQuery)
	if err != nil {
		return err
	}
	defer processlistRows.Close()

	var (
		user            string
		host            string
		command         string
		state           string
		count           uint32
		time            uint32
		memoryUsed      uint32
		totalMemoryUsed uint64
	)
	// Define maps
	stateCounts := make(map[string]map[string]uint32)
	stateTime := make(map[string]map[string]uint32)
	stateHostCounts := make(map[string]uint32)
	stateUserCounts := make(map[string]uint32)

	columnDests := []interface{}{&user, &host, &command, &state, &count, &time}
	if plMemoryUsedExists != nil && *plMemoryUsedExists {
		columnDests = append(columnDests, &memoryUsed)
	}
	for processlistRows.Next() {
		err = processlistRows.Scan(columnDests...)
		if err != nil {
			return err
		}
		command = sanitizeState(command)
		state = sanitizeState(state)
		if host == "" {
			host = "unknown"
		}

		// Init maps
		if _, ok := stateCounts[command]; !ok {
			stateCounts[command] = make(map[string]uint32)
			stateTime[command] = make(map[string]uint32)
		}
		if _, ok := stateCounts[command][state]; !ok {
			stateCounts[command][state] = 0
			stateTime[command][state] = 0
		}
		if _, ok := stateHostCounts[host]; !ok {
			stateHostCounts[host] = 0
		}
		if _, ok := stateUserCounts[user]; !ok {
			stateUserCounts[user] = 0
		}

		stateCounts[command][state] += count
		stateTime[command][state] += time
		stateHostCounts[host] += count
		stateUserCounts[user] += count
	}

	for _, command := range slices.Sorted(maps.Keys(stateCounts)) {
		for _, state := range slices.Sorted(maps.Keys(stateCounts[command])) {
			ch <- prometheus.MustNewConstMetric(processlistCountDesc, prometheus.GaugeValue, float64(stateCounts[command][state]), command, state)
			ch <- prometheus.MustNewConstMetric(processlistTimeDesc, prometheus.GaugeValue, float64(stateTime[command][state]), command, state)
		}
	}

	if s.ProcessesByHost {
		for _, host := range slices.Sorted(maps.Keys(stateHostCounts)) {
			ch <- prometheus.MustNewConstMetric(processesByHostDesc, prometheus.GaugeValue, float64(stateHostCounts[host]), host)
		}
	}
	if s.ProcessesByUser {
		for _, user := range slices.Sorted(maps.Keys(stateUserCounts)) {
			ch <- prometheus.MustNewConstMetric(processesByUserDesc, prometheus.GaugeValue, float64(stateUserCounts[user]), user)
		}
	}
	ch <- prometheus.MustNewConstMetric(processlistMemoryUsedDesc, prometheus.GaugeValue, float64(totalMemoryUsed))

	return nil
}

func sanitizeState(state string) string {
	if state == "" {
		state = "unknown"
	}
	state = strings.ToLower(state)
	replacements := map[string]string{
		";": "",
		",": "",
		":": "",
		".": "",
		"(": "",
		")": "",
		" ": "_",
		"-": "_",
	}
	for r := range replacements {
		state = strings.ReplaceAll(state, r, replacements[r])
	}
	return state
}

func checkPLMemoryUsedColumn(ctx context.Context, db *sql.DB, logger *slog.Logger) {
	if plMemoryUsedExists != nil {
		return
	}

	plColumnsRows, err := db.QueryContext(ctx, infoSchemaProcesslistColumnsQuery)
	if err != nil {
		logger.Error("Failed to get column names of information_schema.processlist: ", err)
		return
	}
	defer plColumnsRows.Close()

	var columnName string
	boolVal := false
	for plColumnsRows.Next() {
		err = plColumnsRows.Scan(&columnName)
		if err != nil {
			logger.Error("Failed to get column names of information_schema.processlist: ", err)
			return
		}

		if strings.ToLower(columnName) == "memory_used" {
			boolVal = true
			plMemoryUsedSelect = ",sum(memory_used)"
			break
		}
	}

	plMemoryUsedExists = &boolVal
}

// check interface
var _ Scraper = ScrapeProcesslist{}
