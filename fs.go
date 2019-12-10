package main

import (
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/skratchdot/open-golang/open"
)

var (
	port             int
	help             bool
	directory        string
	upDirectory      string
	timeout          int
	noQRCode         bool
	noDir            bool
	baseURI          string
	suffix           string
	filenameContains string
	wg               sync.WaitGroup
)

type FromTo struct {
	FromPC string
	ToPC   string
}

type Head struct {
	FromTo
	NoQrcode bool
	ToQrcode string
	ToIndex  string
	Title    string
	FontSize int
}

type UpResult struct {
	Head
	OkFiles     string
	FailedFiles string
	FilePath    string
}

const (
	qrPattern        = "/qrcode"
	filePattern      = "/file/"
	indexPattern     = "/index"
	uploadPattern    = "/upload"
	maxUploadFileNum = 5
)

func init() {
	flag.IntVar(&port, "p", 8000, "Listen port")
	flag.BoolVar(&help, "h", false, "Print this help infomation")
	flag.StringVar(&directory, "d", ".", "File server root path")
	flag.StringVar(&upDirectory, "u", ".", "Upload files root path")
	flag.StringVar(&suffix, "s", "", "Suffix of filename, only matched file will be shown")
	flag.StringVar(&filenameContains, "f", "", "Substring of filename, only matched file/dirs will be shown")
	flag.IntVar(&timeout, "t", 5, "Select timeout in seconds, when you have more than 1 NIC, you need to select one, or we will use all the NICs")
	flag.BoolVar(&noQRCode, "n", false, "Only serve file, do not generate and open QR code")
	flag.BoolVar(&noDir, "nd", false, "Do not show directory, serve only files")
}

func main() {
	flag.Parse()
	if help {
		fmt.Printf("Usage: %s [options]\n\n", os.Args[0])
		flag.PrintDefaults()
		return
	}

	ips := getIPs()
	ip := selectInterface(ips)
	host := fmt.Sprintf("%s:%d", ip, port)
	baseURI = "http://" + host

	if noQRCode == false {
		http.HandleFunc(qrPattern, func(w http.ResponseWriter, r *http.Request) {
			b, err := qrcode.Encode(baseURI+indexPattern, qrcode.Highest, 256)
			if err != nil {
				log.Fatal(err)
			} else {
				w.Write(b)
			}
		})
	}

	http.Handle(filePattern, http.StripPrefix(filePattern, wrapFSHandler(FileServer(http.Dir(directory)))))
	http.HandleFunc(uploadPattern, uploadHandler)
	http.HandleFunc(indexPattern, indexHandler)

	log.Printf("Listen at %s\n", host)
	log.Printf("Access files by http://%s\n", host+filePattern)

	if noQRCode == false {
		open.Run(baseURI + qrPattern)
	}
	log.Fatal(http.ListenAndServe(host, nil))
}

func wrapFSHandler(h http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := []rune(r.RequestURI)
		if s[len(s)-1] == '/' {
			t, _ := template.New("head").Parse(customHead)
			t.Execute(w, Head{FromTo{"", baseURI + uploadPattern}, noQRCode, baseURI + qrPattern, baseURI + indexPattern, "Get Files", 300})
			h.ServeHTTP(w, r)
		} else {
			h.ServeHTTP(w, r)
		}
	}
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	t, _ := template.New("index").Parse(indexTemplate)
	//t.Execute(w, FromTo{baseURI + filePattern, baseURI + uploadPattern})
	t.Execute(w, Head{FromTo{baseURI + filePattern, ""}, noQRCode, baseURI + qrPattern, baseURI + uploadPattern, "Index Page", 300})
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		t, _ := template.New("up").Parse(uploadTemplate)
		t.Execute(w, Head{FromTo{baseURI + filePattern, ""}, noQRCode, baseURI + qrPattern, baseURI + indexPattern, "Upload Files", 300})
	} else {
		r.ParseMultipartForm(32 << 20)

		wg.Add(maxUploadFileNum)

		type upStat struct {
			name string
			isOk bool
		}
		okFiles := ""
		failedFiles := ""
		upStatCh := make(chan upStat)
		go func() {
			for {
				if v, ok := <-upStatCh; ok {
					if v.isOk {
						okFiles += "," + v.name
					} else {
						failedFiles += "," + v.name
					}
				} else {
					break
				}
			}
		}()

		for i := 1; i <= maxUploadFileNum; i++ {
			go func(idx int) {
				defer wg.Done()
				file, handler, err := r.FormFile(fmt.Sprintf("upfile%d", idx))
				if err != nil {
					log.Println(err)
					return
				}
				defer file.Close()
				fname := handler.Filename
				if fname == "" {
					return // not selected
				}

				upFilePath := filepath.Join(upDirectory, fname)
				f, err := os.OpenFile(upFilePath, os.O_WRONLY|os.O_CREATE, 0666)
				if err != nil {
					upStatCh <- upStat{fname, false}
					log.Println(err)
					return
				}
				defer f.Close()
				io.Copy(f, file)
				upStatCh <- upStat{fname, true}
			}(i)
		}

		wg.Wait()
		close(upStatCh)

		absPath, _ := filepath.Abs(upDirectory)
		t, _ := template.New("result").Parse(upResultTemplate)
		okFiles = strings.Replace(okFiles, ",", "", 1)
		failedFiles = strings.Replace(failedFiles, ",", "", 1)
		t.Execute(w, UpResult{Head{FromTo{"", baseURI + uploadPattern}, noQRCode, baseURI + qrPattern, baseURI + indexPattern, "Upload Result", 300}, okFiles, failedFiles, absPath})
	}
}

func selectInterface(ips map[string]string) string {
	length := len(ips)
	ch := make(chan int, 1)

	switch {
	case length < 1:
		log.Fatal("Can not get local ip")
	case length == 1:
		for _, v := range ips {
			return v
		}
	default:
		keys := keys(ips)
		go readUserInput(keys, ips, ch)
		select {
		case <-time.After(time.Second * time.Duration(timeout)):
			fmt.Println()
			log.Printf("Input timeout, using %s\t%s\n", keys[0], ips[keys[0]])
			return ips[keys[0]]
		case input, ok := <-ch:
			if ok && input >= 0 && input < len(keys) {
				fmt.Printf("Using %s\t%s\n", keys[input], ips[keys[input]])
				return ips[keys[input]]
			} else {
				log.Fatal("Invalid index.")
			}
		}
	}

	return ""
}

func readUserInput(keys []string, ips map[string]string, ch chan int) {
	defer func() { close(ch) }()
	fmt.Printf("You hava more than 1 NIC, please select one, or we listen on all the NICs.\n\n")
	for i, v := range keys {
		fmt.Printf("%2d\t%-16s\t%-s\n", i, v, ips[v])
	}

	fmt.Printf("Please input the interface index[0]: ")
	var idx int
	fmt.Scanf("%d", &idx)
	ch <- idx
}

func keys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
