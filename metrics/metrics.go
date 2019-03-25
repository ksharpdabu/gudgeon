package metrics

import (
	"database/sql"
	"math"
	"net"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/GeertJohan/go.rice"
	"github.com/json-iterator/go"
	"github.com/miekg/dns"
	"github.com/shirou/gopsutil/mem"
	"github.com/shirou/gopsutil/process"
	log "github.com/sirupsen/logrus"

	"github.com/chrisruffalo/gudgeon/config"
	"github.com/chrisruffalo/gudgeon/db"
	"github.com/chrisruffalo/gudgeon/resolver"
	"github.com/chrisruffalo/gudgeon/rule"
	"github.com/chrisruffalo/gudgeon/util"
)

const (
	// metrics prefix
	MetricsPrefix = "gudgeon-"
	// metrics names are prefixed by the metrics prefix and delim
	TotalRules             = "active-rules"
	TotalQueries           = "total-session-queries"
	TotalLifetimeQueries   = "total-lifetime-queries"
	TotalIntervalQueries   = "total-interval-queries"
	CachedQueries          = "cached-queries"
	BlockedQueries         = "blocked-session-queries"
	BlockedLifetimeQueries = "blocked-lifetime-queries"
	BlockedIntervalQueries = "blocked-interval-queries"
	QueryTime              = "query-time"
	// cache entries
	CurrentCacheEntries = "cache-entries"
	// rutnime metrics
	GoRoutines         = "goroutines"
	Threads            = "process-threads"
	CurrentlyAllocated = "allocated-bytes"    // heap allocation in go runtime stats
	UsedMemory         = "process-used-bytes" // from the process api
	FreeMemory         = "free-memory-bytes"
	SystemMemory       = "system-memory-bytes"
	// cpu metrics
	CPUHundredsPercent = "cpu-hundreds-percent" // 17 == 0.17 percent, expressed in integer terms
)

type metricsInfo struct {
	address  string
	request  *dns.Msg
	response *dns.Msg
	result   *resolver.ResolutionResult
	rCon     *resolver.RequestContext
}

type MetricsEntry struct {
	FromTime        time.Time
	AtTime          time.Time
	Values          map[string]*Metric
	IntervalSeconds int
}

type Metric struct {
	Count int64 `json:"count"`
}

func (metric *Metric) Set(newValue int64) *Metric {
	metric.Count = newValue
	return metric
}

func (metric *Metric) Inc(byValue int64) *Metric {
	metric.Count = metric.Count + byValue
	return metric
}

func (metric *Metric) Clear() *Metric {
	metric.Set(0)
	return metric
}

func (metric *Metric) Value() int64 {
	return metric.Count
}

type metrics struct {
	// keep config
	config *config.GudgeonConfig

	metricsMap   map[string]*Metric
	metricsMutex sync.RWMutex

	metricsInfoChan chan *metricsInfo
	db              *sql.DB

	detailTx   *sql.Tx
	detailStmt *sql.Stmt

	cacheSizeFunc CacheSizeFunction

	// time management for interval insert
	lastInsert time.Time
	ticker     *time.Ticker
	doneTicker chan bool
}

type CacheSizeFunction = func() int64

type Metrics interface {
	GetAll() map[string]*Metric
	Get(name string) *Metric

	// use cache function
	UseCacheSizeFunction(function CacheSizeFunction)

	// record relevant metrics based on request
	RecordQueryMetrics(ip *net.IP, request *dns.Msg, response *dns.Msg, rCon *resolver.RequestContext, result *resolver.ResolutionResult)

	// Query metrics from db
	Query(start time.Time, end time.Time) ([]*MetricsEntry, error)
	QueryStream(returnChan chan *MetricsEntry, start time.Time, end time.Time) error

	// top information
	TopClients(limit int) []*TopInfo
	TopDomains(limit int) []*TopInfo
	TopQueryTypes(limit int) []*TopInfo
	TopLists(limit int) []*TopInfo
	TopRules(limit int) []*TopInfo

	// stop the metrics collection
	Stop()
}

// write all metrics out to encoder
var json = jsoniter.Config{
	EscapeHTML:                    false,
	MarshalFloatWith6Digits:       true,
	ObjectFieldMustBeSimpleString: true,
	SortMapKeys:                   false,
	ValidateJsonRawMessage:        true,
	DisallowUnknownFields:         false,
}.Froze()

func New(config *config.GudgeonConfig) Metrics {
	metrics := &metrics{
		config:     config,
		metricsMap: make(map[string]*Metric),
	}

	if *(config.Metrics.Persist) {
		// get path to long-standing data ({home}/'data') and make sure it exists
		dataDir := config.DataRoot()
		if _, err := os.Stat(dataDir); os.IsNotExist(err) {
			os.MkdirAll(dataDir, os.ModePerm)
		}

		// open db
		dbDir := path.Join(dataDir, "metrics")
		// create directory
		if _, err := os.Stat(dbDir); os.IsNotExist(err) {
			os.MkdirAll(dbDir, os.ModePerm)
		}

		// get path to db
		dbPath := path.Join(dbDir, "metrics.db")

		// do migrations
		migrationsBox := rice.MustFindBox("metrics-migrations")

		// open database
		var err error
		metrics.db, err = db.OpenAndMigrate(dbPath, "", migrationsBox)
		if err != nil {
			log.Errorf("Metrics: %s", err)
			return nil
		}

		// init lifetime metric counts
		metrics.load()

		// flush any outstanding metrics in the temp table
		metrics.flushDetailedMetrics()

		// prune metrics after load (in case the service has been down longer than the prune interval)
		metrics.prune()
	}

	// update metrics initially
	metrics.update()

	// create channel for incoming metrics and start recorder
	metrics.metricsInfoChan = make(chan *metricsInfo, 100000)
	go metrics.worker()

	return metrics
}

func (metrics *metrics) worker() {
	// start ticker to persist data and update periodic metrics
	duration, _ := util.ParseDuration(metrics.config.Metrics.Interval)
	metrics.ticker = time.NewTicker(duration)
	metrics.doneTicker = make(chan bool)
	metrics.lastInsert = time.Now()

	// flush metrics if db is function
	pruneDuration := 1 * time.Hour
	detailFlushDuration := 5 * time.Second

	flushTimer := time.NewTimer(detailFlushDuration)
	defer flushTimer.Stop()
	// prune every hour (also prunes on startup)
	pruneTimer := time.NewTimer(pruneDuration)
	defer pruneTimer.Stop()


	// only stop these timers if they are not needed
	if metrics.db == nil {
		pruneTimer.Stop()
	}
	if !*metrics.config.Metrics.Detailed {
		flushTimer.Stop()
	}

	// stop the normal metrics ticker that records and inserts records
	defer metrics.ticker.Stop()

	for {
		select {
		case <-metrics.ticker.C:
			// update periodic metrics
			metrics.update()

			// only insert/prune if a db exists
			if metrics.db != nil {
				// insert new metrics
				metrics.insert(time.Now())
			}
		case <-pruneTimer.C:
			metrics.prune()
			pruneTimer.Reset(pruneDuration)
		case <-flushTimer.C:
			metrics.flushDetailedMetrics()
			flushTimer.Reset(detailFlushDuration)
		case info := <-metrics.metricsInfoChan:
			metrics.record(info)
		case <-metrics.doneTicker:
			defer func() { metrics.doneTicker <- true }()
			return
		}
	}
}

func (metrics *metrics) GetAll() map[string]*Metric {
	metrics.metricsMutex.RLock()
	mapCopy := make(map[string]*Metric, 0)
	for k, v := range metrics.metricsMap {
		mapCopy[k] = &Metric{Count: v.Value()}
	}
	metrics.metricsMutex.RUnlock()
	return mapCopy
}

func (metrics *metrics) Get(name string) *Metric {
	metrics.metricsMutex.RLock()
	if metric, found := metrics.metricsMap[MetricsPrefix+name]; found {
		defer metrics.metricsMutex.RUnlock()
		return metric
	}
	metrics.metricsMutex.RUnlock()
	metrics.metricsMutex.Lock()
	defer metrics.metricsMutex.Unlock()

	metric := &Metric{Count: 0}
	metrics.metricsMap[MetricsPrefix+name] = metric
	return metric
}

func (metrics *metrics) update() {
	// get pid
	pid := os.Getpid()

	// get process
	process, err := process.NewProcess(int32(pid))
	if err == nil && process != nil {
		if percent, err := process.CPUPercent(); err == nil {
			metrics.Get(CPUHundredsPercent).Set(int64(percent * 100))
		}
		if pmem, err := process.MemoryInfo(); err == nil {
			metrics.Get(UsedMemory).Set(int64(pmem.RSS))
		}
		if threads, err := process.NumThreads(); err == nil {
			metrics.Get(Threads).Set(int64(threads))
		}
		if vmem, err := mem.VirtualMemory(); err == nil {
			metrics.Get(FreeMemory).Set(int64(vmem.Free))
			metrics.Get(SystemMemory).Set(int64(vmem.Total))
		}
	}

	// capture goroutines
	metrics.Get(GoRoutines).Set(int64(runtime.NumGoroutine()))

	// capture memory metrics
	memoryStats := &runtime.MemStats{}
	runtime.ReadMemStats(memoryStats)
	metrics.Get(CurrentlyAllocated).Set(int64(memoryStats.Alloc))

	// capture cache size
	if metrics.cacheSizeFunc != nil {
		metrics.Get(CurrentCacheEntries).Set(metrics.cacheSizeFunc())
	}
}

func (metrics *metrics) record(info *metricsInfo) {
	// update specific/detailed metrics
	metrics.updateDetailedMetrics(info)

	// first add count to total queries
	metrics.Get(TotalQueries).Inc(1)
	metrics.Get(TotalLifetimeQueries).Inc(1)
	metrics.Get(TotalIntervalQueries).Inc(1)

	// add cache hits
	if info.result != nil && info.result.Cached {
		metrics.Get(CachedQueries).Inc(1)
	}

	// add blocked queries
	if info.result != nil && (info.result.Blocked || info.result.Match == rule.MatchBlock) {
		metrics.Get(BlockedQueries).Inc(1)
		metrics.Get(BlockedLifetimeQueries).Inc(1)
		metrics.Get(BlockedIntervalQueries).Inc(1)

		if info.result.MatchList != nil {
			metrics.Get("rules-session-matched-" + info.result.MatchList.ShortName()).Inc(1)
			metrics.Get("rules-lifetime-matched-" + info.result.MatchList.ShortName()).Inc(1)
		}
	}
}

func (metrics *metrics) insert(currentTime time.Time) {
	if metrics.detailTx != nil {
		defer metrics.detailTx.Rollback()
		err := metrics.detailTx.Commit()
		metrics.detailTx = nil
		if err != nil {
			log.Errorf("Could not commit pending metrics temp entries: %s", err)
			return
		}
	}

	// get all metrics
	all := metrics.GetAll()

	// make into json string
	bytes, err := json.Marshal(all)
	if err != nil {
		log.Errorf("Error marshalling metrics json: %s", err)
		return
	}

	stmt := "INSERT INTO metrics (FromTime, AtTime, MetricsJson, IntervalSeconds) VALUES (?, ?, ?, ?)"
	pstmt, err := metrics.db.Prepare(stmt)
	if err != nil {
		log.Errorf("Error preparing metrics statement: %s", err)
		return
	}
	defer pstmt.Close()

	_, err = pstmt.Exec(metrics.lastInsert, currentTime, string(bytes), int(math.Round(currentTime.Sub(metrics.lastInsert).Seconds())))
	if err != nil {
		log.Errorf("Error executing metrics statement: %s", err)
		return
	}

	// clear and restart interval
	metrics.Get(TotalIntervalQueries).Clear()
	metrics.Get(BlockedIntervalQueries).Clear()
	metrics.lastInsert = currentTime
}

func (metrics *metrics) prune() {
	duration, _ := util.ParseDuration(metrics.config.Metrics.Duration)
	_, err := metrics.db.Exec("DELETE FROM metrics WHERE AtTime <= ?", time.Now().Add(-1*duration))
	if err != nil {
		log.Errorf("Error pruning metrics data: %s", err)
	}
}

// allows the same query and row scan logic to share code
type queryAccumulator = func(entry *MetricsEntry)

// implementation of the underlying query function
func (metrics *metrics) query(qA queryAccumulator, start time.Time, end time.Time) error {
	// don't do anything with nil accumulator
	if qA == nil {
		return nil
	}

	rows, err := metrics.db.Query("SELECT FromTime, AtTime, MetricsJson, IntervalSeconds FROM metrics WHERE FromTime >= ? AND AtTime <= ? ORDER BY AtTime ASC", start, end)
	if err != nil {
		return err
	}
	defer rows.Close()

	var (
		metricsJsonString string
	)

	var me *MetricsEntry
	for rows.Next() {
		me = &MetricsEntry{}

		err = rows.Scan(&me.AtTime, &me.FromTime, &metricsJsonString, &me.IntervalSeconds)
		if err != nil {
			log.Errorf("Error scanning for metrics query: %s", err)
			continue
		}
		// unmarshal string into values
		json.Unmarshal([]byte(metricsJsonString), &me.Values)

		// call accumulator function
		qA(me)
	}

	return nil
}

// traditional query that returns an arry of metrics entries, good for testing, small queries
func (metrics *metrics) Query(start time.Time, end time.Time) ([]*MetricsEntry, error) {
	entries := make([]*MetricsEntry, 0, 100)
	acc := func(me *MetricsEntry) {
		if me == nil {
			return
		}
		entries = append(entries, me)
	}
	err := metrics.query(acc, start, end)
	return entries, err
}

// less traditional query type that allows the web endpoint to stream the json back out as rows are scanned
func (metrics *metrics) QueryStream(returnChan chan *MetricsEntry, start time.Time, end time.Time) error {
	acc := func(me *MetricsEntry) {
		if me == nil {
			return
		}
		returnChan <- me
	}
	err := metrics.query(acc, start, end)
	close(returnChan)
	return err
}

func (metrics *metrics) load() {
	rows, err := metrics.db.Query("SELECT MetricsJson FROM metrics ORDER BY AtTime DESC LIMIT 1")
	if err != nil {
		log.Errorf("Could not load initial metrics information: %s", err)
		return
	}
	defer rows.Close()

	var metricsJsonString string
	for rows.Next() {
		err = rows.Scan(&metricsJsonString)
		if err != nil {
			log.Errorf("Error scanning for metrics results: %s", err)
			continue
		}
		if "" != metricsJsonString {
			break
		}
	}

	// can't do anything with empty string, set, or object
	metricsJsonString = strings.TrimSpace(metricsJsonString)
	if "" == metricsJsonString || "{}" == metricsJsonString || "[]" == metricsJsonString {
		return
	}

	// unmarshal object
	var data map[string]*Metric
	json.Unmarshal([]byte(metricsJsonString), &data)

	// load any metric that has "lifetime" in the key
	// from the database so that we can manage rules
	// this way as well
	for key, metric := range data {
		if strings.Contains(key, "lifetime") {
			metrics.Get(key[len(MetricsPrefix):]).Set(metric.Value())
		}
	}
}

func (metrics *metrics) UseCacheSizeFunction(function CacheSizeFunction) {
	metrics.cacheSizeFunc = function
}

func (metrics *metrics) RecordQueryMetrics(ip *net.IP, request *dns.Msg, response *dns.Msg, rCon *resolver.RequestContext, result *resolver.ResolutionResult) {
	msg := new(metricsInfo)
	if ip != nil {
		msg.address = ip.String()
	}
	msg.request = request
	msg.response = response
	msg.result = result
	msg.rCon = rCon
	metrics.metricsInfoChan <- msg
}

func (metrics *metrics) Stop() {
	// wait for done ticker
	metrics.doneTicker <- true
	<-metrics.doneTicker
	close(metrics.doneTicker)

	// close metrics info channel
	close(metrics.metricsInfoChan)

	// close db and shutdown timer if it exists
	if metrics.db != nil {
		// insert one last time
		metrics.insert(time.Now())
		metrics.prune()

		// flush outstanding details
		metrics.flushDetailedMetrics()
		if metrics.detailStmt != nil {
			metrics.detailStmt.Close()
		}

		// close db
		metrics.db.Close()
	}
}
