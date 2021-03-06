package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	"io/ioutil"

	"github.com/caarlos0/env"
	"github.com/ewilde/kubecon/cmd/http-echo/version"
)

type config struct {
	DisableZipkin bool `env:"DISABLE_ZIPKIN"`
}

var (
	listenFlag              = flag.String("listen", ":5678", "address and port to listen")
	textFlag                = flag.String("text", "", "text to put on the webpage")
	responseCodeFlag        = flag.Int("response-code", 200, "response code to return")
	responseCodeRate        = flag.Float64("response-rate", 100.0, "percentage of time to return -responseCode, default to 200 for other results")
	versionFlag             = flag.Bool("version", false, "display version information")
	delayFlag               = flag.Float64("response-delay", 0, "delay request in ms every -response-rate")
	ipAddress        net.IP = nil
	// stdoutW and stderrW are for overriding in test.
	stdoutW = os.Stdout
	stderrW = os.Stderr
)

func init() {
	rand.Seed(time.Now().UnixNano())
	ipAddress = getOutboundIP()
}

func main() {
	flag.Parse()
	// Asking for the version?
	if *versionFlag {
		fmt.Fprintln(stderrW, version.HumanVersion)
		os.Exit(0)
	}

	// Validation
	if *textFlag == "" {
		fmt.Fprintln(stderrW, "Missing -text option!")
		os.Exit(127)
	}

	args := flag.Args()
	if len(args) > 0 {
		fmt.Fprintln(stderrW, "Too many arguments!")
		os.Exit(127)
	}

	NewServer(*textFlag, *responseCodeFlag, *responseCodeRate, *delayFlag)
}

func NewServer(text string, code int, rate float64, delay float64) {

	cfg := config{}
	err := env.Parse(&cfg)
	if err != nil {
		log.Fatalf("Could not parse environment configuration: %v", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", httpLog(stdoutW, withAppHeaders(httpEcho(stdoutW, text, code, rate, delay, cfg))))
	// Health endpoint
	mux.HandleFunc("/health", withAppHeaders(httpHealth()))
	server := &http.Server{
		Addr:    *listenFlag,
		Handler: mux,
	}
	serverCh := make(chan struct{})
	go func() {
		log.Printf("[INFO] server is listening on %s\n", *listenFlag)
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("[ERR] server exited with: %s", err)
		}
		close(serverCh)
	}()
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt)
	// Wait for interrupt
	<-signalCh
	log.Printf("[INFO] received interrupt, shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("[ERR] failed to shutdown server: %s", err)
	}
	// If we got this far, it was an interrupt, so don't exit cleanly
	os.Exit(2)
}

func httpEcho(logOut io.Writer, text string, code int, rate float64, delay float64, config config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		delayTime := delay
		defer func(begin time.Time) {
			if config.DisableZipkin {
				return
			}

			if err := trace(r, logOut, "http-echo", "select * from Orders;", time.Since(begin)); err != nil {
				fmt.Fprintf(logOut, "%v", err)
			}
		}(time.Now().UTC())

		if r.URL.Query().Get("status") != "" {
			status, err := strconv.Atoi(r.URL.Query().Get("status"))
			if err == nil {
				w.WriteHeader(status)
			}
		}

		setResponseCode(code, rate, w)

		if r.URL.Query().Get("response-delay") != "" {
			delayInt, _ := strconv.Atoi(r.URL.Query().Get("response-delay"))
			delayTime = float64(delayInt)
		}

		setTimeout(delayTime, rate)

		fmt.Fprintln(w, text)
		body, _ := ioutil.ReadAll(r.Body)
		if len(body) > 0 {
			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "Body")
			fmt.Fprintln(w, "----")
			fmt.Fprintln(w, string(body))
		}
	}
}

func setResponseCode(code int, rate float64, w http.ResponseWriter) {
	if code != 200 {
		if rand.Float64() <= rate/100 {
			w.WriteHeader(code)
		}
	}
}

func setTimeout(delay float64, rate float64) {
	if delay != 0 {
		if rand.Float64() <= rate/100 {
			duration := time.Duration(float64(time.Millisecond) * delay)
			log.Printf("[INFO] Timeout delay %s.", duration.String())
			time.Sleep(duration)
		}
	}
}

func httpHealth() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"status":"ok"}`)
	}
}

func valueOrDefault(value, def string) string {
	if value != "" {
		return value
	}
	return def
}
