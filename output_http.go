package main

import (
	"bufio"
	"bytes"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

type RedirectNotAllowed struct{}

func (e *RedirectNotAllowed) Error() string {
	return "Redirects not allowed"
}

// customCheckRedirect disables redirects https://github.com/buger/gor/pull/15
func customCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 0 {
		return new(RedirectNotAllowed)
	}
	return nil
}

// ParseRequest in []byte returns a http request or an error
func ParseRequest(data []byte) (request *http.Request, err error) {
	buf := bytes.NewBuffer(data)
	reader := bufio.NewReader(buf)

	request, err = http.ReadRequest(reader)

	return
}

const InitialDynamicWorkers = 10

type HTTPOutput struct {
	address string
	limit   int
	queue   chan []byte

	activeWorkers int64
	needWorker    chan int

	urlRegexp            HTTPUrlRegexp
	headerFilters        HTTPHeaderFilters
	headerHashFilters    HTTPHeaderHashFilters
	outputHTTPUrlRewrite UrlRewriteMap

	headers HTTPHeaders
	methods HTTPMethods

	elasticSearch *ESPlugin

	queueStats   *GorStat
	queueALStats *ALStat
}

func NewHTTPOutput(address string, headers HTTPHeaders, methods HTTPMethods, urlRegexp HTTPUrlRegexp, headerFilters HTTPHeaderFilters, headerHashFilters HTTPHeaderHashFilters, elasticSearchAddr string, outputHTTPUrlRewrite UrlRewriteMap) io.Writer {

	o := new(HTTPOutput)

	if !strings.HasPrefix(address, "http") {
		address = "http://" + address
	}

	o.address = address
	o.headers = headers
	o.methods = methods

	o.urlRegexp = urlRegexp
	o.headerFilters = headerFilters
	o.headerHashFilters = headerHashFilters
	o.outputHTTPUrlRewrite = outputHTTPUrlRewrite

	o.queue = make(chan []byte, 100)
	if Settings.outputHTTPStats {
		o.queueStats = NewGorStat("output_http")
	}
	if Settings.stats {
		o.queueALStats = NewALStat()
	}

	o.needWorker = make(chan int, 1)

	// Initial workers count
	if Settings.outputHTTPWorkers == -1 {
		o.needWorker <- InitialDynamicWorkers
	} else {
		o.needWorker <- Settings.outputHTTPWorkers
	}

	if elasticSearchAddr != "" {
		o.elasticSearch = new(ESPlugin)
		o.elasticSearch.Init(elasticSearchAddr)
	}

	go o.WorkerMaster()

	return o
}

func (o *HTTPOutput) WorkerMaster() {
	for {
		new_workers := <-o.needWorker
		for i := 0; i < new_workers; i++ {
			go o.Worker()
		}

		// Disable dynamic scaling if workers poll fixed size
		if Settings.outputHTTPWorkers != -1 {
			return
		}
	}
}

func (o *HTTPOutput) Worker() {
	tr := &http.Transport{DisableKeepAlives: false}
	client := &http.Client{
		CheckRedirect: customCheckRedirect,
		Transport:     tr,
	}

	death_count := 0

	atomic.AddInt64(&o.activeWorkers, 1)

	for {
		select {
		case data := <-o.queue:
			o.sendRequest(client, data)
			death_count = 0
		case <-time.After(time.Millisecond * 100):
			// When dynamic scaling enabled workers die after 2s of inactivity
			if Settings.outputHTTPWorkers == -1 {
				death_count += 1
			} else {
				continue
			}

			if death_count > 20 {
				workersCount := atomic.LoadInt64(&o.activeWorkers)

				// At least 1 worker should be alive
				if workersCount != 1 {
					atomic.AddInt64(&o.activeWorkers, -1)
					return
				}
			}
		}
	}
}

func (o *HTTPOutput) Write(data []byte) (n int, err error) {
	buf := make([]byte, len(data))
	copy(buf, data)

	o.queue <- buf

	if Settings.outputHTTPStats {
		o.queueStats.Write(len(o.queue))
	}

	if Settings.outputHTTPWorkers == -1 {
		workersCount := atomic.LoadInt64(&o.activeWorkers)

		if len(o.queue) > int(workersCount) {
			o.needWorker <- len(o.queue)
		}
	}

	return len(data), nil
}

func (o *HTTPOutput) sendRequest(client *http.Client, data []byte) {
	request, err := ParseRequest(data)

	if err != nil {
		//log.Println("Cannot parse request", string(data), err)
		return
	}

	if len(o.methods) > 0 && !o.methods.Contains(request.Method) {
		return
	}

	if !(o.urlRegexp.Good(request) && o.headerFilters.Good(request) && o.headerHashFilters.Good(request)) {
		return
	}

	// Rewrite the path as necessary
	request.URL.Path = o.outputHTTPUrlRewrite.Rewrite(request.URL.Path)

	// Change HOST of original request
	URL := o.address + request.URL.Path + "?" + request.URL.RawQuery

	request.RequestURI = ""
	request.URL, _ = url.ParseRequestURI(URL)

	for _, header := range o.headers {
		SetHeader(request, header.Name, header.Value)
	}

	start := time.Now()
	resp, err := client.Do(request)
	stop := time.Now()
	elapsed := time.Since(start)

	// We should not count Redirect as errors
	if urlErr, ok := err.(*url.Error); ok {
		if _, ok := urlErr.Err.(*RedirectNotAllowed); ok {
			err = nil
		}
	}

	if err == nil {
		o.queueALStats.Write(int(elapsed / 1e3))
		defer resp.Body.Close()
		_, _ = ioutil.ReadAll(resp.Body)

	} else {
		log.Println("Request error:", err)
	}

	if o.elasticSearch != nil {
		o.elasticSearch.ResponseAnalyze(request, resp, start, stop)
	}
	//fmt.Println(timedelta.Nanoseconds() / 1e6)
	//log.Println(timedelta)
}

func SetHeader(request *http.Request, name string, value string) {
	// Need to check here for the Host header as it needs to be set on the request and not as a separate header
	// http.ReadRequest sets it by default to the URL Host of the request being read
	if name == "Host" {
		request.Host = value
	} else {
		request.Header.Set(name, value)
	}

	return

}

func (o *HTTPOutput) String() string {
	return "HTTP output: " + o.address
}
