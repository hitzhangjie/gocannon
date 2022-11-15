package lib

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/valyala/fasthttp"

	"github.com/kffl/gocannon/common"
)

// Gocannon represents a single gocannon instance with a config defined upon its creation.
type Gocannon struct {
	cfg    common.Config
	client *fasthttp.HostClient
	stats  statsCollector
	plugin common.GocannonPlugin
}

// NewGocannon creates a new gocannon instance using a provided config.
func NewGocannon(cfg common.Config) (Gocannon, error) {
	var err error

	gocannon := Gocannon{cfg: cfg}

	if *cfg.Plugin != "" {
		gocannonPlugin, err := loadPlugin(*cfg.Plugin, *cfg.Format != "default")
		if err != nil {
			return gocannon, err
		}
		gocannon.plugin = gocannonPlugin
		gocannonPlugin.Startup(cfg)
	}

	c, err := newHTTPClient(*cfg.Target, *cfg.Timeout, *cfg.Connections, *cfg.TrustAll, true)

	if err != nil {
		return gocannon, err
	}

	gocannon.client = c

	stats, scErr := newStatsCollector(*cfg.Mode, *cfg.Connections, *cfg.Preallocate, *cfg.Timeout)

	gocannon.stats = stats

	if scErr != nil {
		return gocannon, scErr
	}

	return gocannon, nil
}

// Run performs the load test.
func (g Gocannon) Run() (TestResults, error) {

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT) // ctrl-c ctrl-d ctrl-\

	n := *g.cfg.Connections

	var wg sync.WaitGroup

	wg.Add(n)

	start := makeTimestamp()
	stop := start + g.cfg.Duration.Nanoseconds()

	chclose := make(chan int)
	go func() {
		<-ch
		// 更新下stop，不然影响后面的结果统计(不然qps的计算还是按照之前的参数算的，肯定不对啊)
		stop = start + g.cfg.Duration.Nanoseconds()
		close(chclose)
	}()

	for connectionID := 0; connectionID < n; connectionID++ {
		go func(c *fasthttp.HostClient, cid int, p common.GocannonPlugin) {
			for {
				select {
				case <-chclose:
					goto END
				default:
				}

				var code int
				var start int64
				var end int64
				if p != nil {
					plugTarget, plugMethod, plugBody, plugHeaders := p.BeforeRequest(cid)
					code, start, end = performRequest(
						c,
						plugTarget,
						plugMethod,
						plugBody,
						plugHeaders,
					)
				} else {
					code, start, end = performRequest(c, *g.cfg.Target, *g.cfg.Method, *g.cfg.Body, *g.cfg.Headers)
				}
				if end >= stop {
					break
				}

				g.stats.RecordResponse(cid, code, start, end)
			}
		END:
			wg.Done()
		}(g.client, connectionID, g.plugin)
	}

	wg.Wait()

	err := g.stats.CalculateStats(start, stop, *g.cfg.Interval, *g.cfg.OutputFile)
	if err != nil {
		return nil, err
	}

	return g.stats, err
}
