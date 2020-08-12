// Scrape `information_schema.tables`.

package collector

import (
	"context"
	"database/sql"
	"strconv"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
)

//nolint
func TestScrapeTableSchema(t *testing.T) {
	db, err := sql.Open("mysql", "root@tcp(127.0.0.1:3306)/")
	assert.NoError(t, err)
	defer db.Close()

	name := "test_cache"
	*tableSchemaDatabases = name

	var dbExist int
	err = db.QueryRow("SELECT COUNT(SCHEMA_NAME) FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME = '" + name + "'").Scan(&dbExist)
	assert.NoError(t, err)
	_, err = db.Exec("CREATE DATABASE IF NOT EXISTS " + name)
	assert.NoError(t, err)
	if dbExist == 0 {
		defer func() {
			_, err = db.Exec("DROP DATABASE " + name)
			assert.NoError(t, err)
		}()
	}

	var tableExist int
	err = db.QueryRow("SELECT COUNT(TABLE_NAME) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = '" + name + "' AND TABLE_NAME = '" + name + "'").Scan(&tableExist)
	assert.NoError(t, err)
	_, err = db.Exec("CREATE TABLE IF NOT EXISTS " + name + "." + name + " (id int(64))")
	assert.NoError(t, err)
	if tableExist == 0 {
		defer func() {
			_, err = db.Exec("DROP TABLE " + name + "." + name)
			assert.NoError(t, err)
		}()
	}
	_, err = db.Exec("TRUNCATE " + name + "." + name)
	assert.NoError(t, err)

	rows := []int{1, 2}
	for _, row := range rows {
		_, err = db.Exec("INSERT INTO " + name + "." + name + " VALUES(" + strconv.Itoa(row) + ")")
		assert.NoError(t, err)
		ch := make(chan prometheus.Metric)
		//nolint:wsl
		go func() {
			if err = (ScrapeTableSchema{}).Scrape(context.Background(), db, ch); err != nil {
				t.Errorf("error calling function on test: %s", err)
			}
			close(ch)
		}()

		<-ch
		got := readMetric(<-ch)
		assert.Equal(t, float64(row), got.value)
	}
}
