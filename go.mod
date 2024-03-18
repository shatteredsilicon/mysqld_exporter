module github.com/shatteredsilicon/mysqld_exporter

go 1.20

require (
	github.com/go-sql-driver/mysql v0.0.0-20231031081844-ca087917bf67
	github.com/prometheus/client_golang v0.8.0
	github.com/prometheus/client_model v0.0.0-20171117100541-99fa1f4be8e5
	github.com/prometheus/common v0.0.0-20170616092938-185c63bfc5a8
	github.com/smartystreets/goconvey v1.8.1
	golang.org/x/net v0.22.0
	gopkg.in/DATA-DOG/go-sqlmock.v1 v1.3.0
	gopkg.in/ini.v1 v1.67.0
	gopkg.in/yaml.v2 v2.2.1
)

require (
	github.com/beorn7/perks v0.0.0-20180321164747-3a771d992973 // indirect
	github.com/golang/protobuf v1.1.0 // indirect
	github.com/gopherjs/gopherjs v1.17.2 // indirect
	github.com/jtolds/gls v4.20.0+incompatible // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.1 // indirect
	github.com/prometheus/procfs v0.0.0-20180612222113-7d6f385de8be // indirect
	github.com/sirupsen/logrus v1.8.1 // indirect
	github.com/smarty/assertions v1.15.1 // indirect
	golang.org/x/sys v0.18.0 // indirect
)

replace github.com/go-sql-driver/mysql v0.0.0-20231031081844-ca087917bf67 => github.com/shatteredsilicon/go-sql-driver-mysql v0.0.0-20231031081844-ca087917bf67
