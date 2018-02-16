package main

import (
	"fmt"
	"github.com/alecthomas/kingpin"
	"github.com/tidwall/gjson"
	"gopkg.in/cheggaaa/pb.v2"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const baseURL = "https://konachan.com/post"

type httpRequest struct {
	url  string
	info map[string]string
}

type httpResponse struct {
	url  string
	resp *http.Response
	info map[string]string
}

type Crawler struct {
	query string

	maxWorkers        int
	bufferSize        int
	format            string
	destinationFolder string

	httpClient *http.Client
	pbar       *pb.ProgressBar
}

func (c *Crawler) getURLs() (int, <-chan *httpRequest) {
	ch := make(chan *httpRequest, c.bufferSize)

	response := c.fetch(&httpRequest{fmt.Sprintf("%s.xml?tags=%s", baseURL, c.query), nil})

	xml, _ := ioutil.ReadAll(response.resp.Body)
	response.resp.Body.Close()

	count, _ := strconv.Atoi(string(regexp.MustCompile(`count="(\d+)"`).FindSubmatch(xml)[1]))

	go func() {
		page := 1

		for {
			response := c.fetch(&httpRequest{fmt.Sprintf("%s.json?tags=%s&page=%d", baseURL, c.query, page), nil})

			json, _ := ioutil.ReadAll(response.resp.Body)
			results := gjson.GetManyBytes(json, "#.md5", "#."+c.format)

			if len(results[0].Array()) == 0 {
				close(ch)
				return
			}

			md5, imgUrls := results[0].Array(), results[1].Array()
			for i, url := range imgUrls {
				ch <- &httpRequest{url.String(), map[string]string{"md5": md5[i].String()}}
			}
			response.resp.Body.Close()

			page++
		}
	}()

	return count, ch
}

func (c *Crawler) asyncDownload(requests <-chan *httpRequest) <-chan bool {
	ch := make(chan bool, c.bufferSize)

	go func() {
		var wg sync.WaitGroup
		wg.Add(c.maxWorkers)

		for i := 0; i < c.maxWorkers; i++ {
			go func() {
				for request := range requests {
					ext := regexp.MustCompile(`.[a-z0-9]+$`).FindString(request.url)
					fileBasename := fmt.Sprintf("%s/%s", c.destinationFolder, request.info["md5"])

					matches, _ := filepath.Glob(fileBasename + ".*")
					if matches != nil {
						// fmt.Println(matches)
						ch <- false
						continue
					}

					response := c.fetch(request)
					ch <- c.saver(response, fileBasename+ext)
				}
				wg.Done()
			}()
		}

		wg.Wait()
		close(ch)
	}()

	return ch
}

func (c *Crawler) fetch(request *httpRequest) *httpResponse {
	resp, err := http.Get(request.url)
	if err != nil {
		panic(err)
	}

	return &httpResponse{request.url, resp, request.info}
}

func (c *Crawler) saver(response *httpResponse, filePath string) bool {
	defer response.resp.Body.Close()

	img, err := os.Create(filePath)
	if err != nil {
		panic(err)
	}

	_, err = io.Copy(img, response.resp.Body)
	if err != nil {
		panic(err)
	}
	defer img.Close()

	return true
}

func (c *Crawler) start() <-chan bool {
	total, urls := c.getURLs()
	if total <= 0 {
		fmt.Println("No images.")
		os.Exit(0)
	}
	c.pbar = pb.StartNew(total)

	return c.asyncDownload(urls)
}

func newCrawler(query string, maxWorkers, bufferSize int, timeOut time.Duration, format, destination string) *Crawler {
	client := &http.Client{
		Timeout: timeOut,
	}

	err := os.MkdirAll(destination, os.ModePerm)
	if err != nil {
		panic(err)
	}

	return &Crawler{query, maxWorkers, bufferSize, format + "_url", destination, client, nil}
}

var (
	workers     = kingpin.Flag("workers", "Number of downloaders. Default to 2x CPU threads").Short('w').Default(strconv.Itoa(2 * runtime.NumCPU())).Int()
	bufferSize  = kingpin.Flag("buffer_size", "Buffer size of image URLs queue").Short('b').Default("64").Int()
	timeOut     = kingpin.Flag("time_out", "Time out for image downloading").Short('t').Default("120s").Duration()
	destination = kingpin.Flag("destination", "Image folders").Short('o').Default("images").String()
	rating      = kingpin.Flag("rating", "Rating of the image: safe, questionable, explicit, questionableminus, questionableplus").Short('r').Default("safe").Enum("safe", "questionable", "explicit", "questionableminus", "questionableplus")
	format      = kingpin.Flag("format", "Format of images: preview, sample, file, jpeg").Short('f').Default("file").Enum("preview", "sample", "file", "jpeg")
	tags        = kingpin.Arg("tags", "Searching tags for Konachan").Required().Strings()
)

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	kingpin.Version("0.0.1")
	kingpin.Parse()

	for i, tag := range *tags {
		(*tags)[i] = url.QueryEscape(tag)
	}

	query := fmt.Sprintf("%s+rating:%s", strings.Join(*tags, "+"), *rating)

	startTime := time.Now()

	crawler := newCrawler(query, *workers, *bufferSize, *timeOut, *format, *destination)

	results := crawler.start()

	for range results {
		crawler.pbar.Increment()
	}

	crawler.pbar.Finish()
	fmt.Printf("Total time: %s\n", time.Since(startTime))
}
