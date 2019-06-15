package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	index = template.Must(template.New("index").Parse(
		`<!doctype html>
	 <title>RTL_433 Prometheus Exporter</title>
	 <h1>RTL_433 Prometheus Exporter</h1>
	 <a href="/metrics">Metrics</a>`))

	addr       = flag.String("listen", ":9001", "Address to listen on")
	subprocess = flag.String("subprocess", "rtl_433 -F json", "What command to run to get rtl_433 radio packets")

	labels = []string{"model", "id", "channel"}

	packetsReceived = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rtl_433_packets_received",
			Help: "Packets (temperature messages) received.",
		},
		labels,
	)
	temperature = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rtl_433_temperature_celsius",
			Help: "Temperature in Celsius",
		},
		labels,
	)
	humidity = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rtl_433_humidity",
			Help: "Relative Humidity (0-1.0)",
		},
		labels,
	)
	timestamp = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rtl_433_timestamp_seconds",
			Help: "Timestamp we received the message (Unix seconds)",
		},
		labels,
	)
	battery = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rtl_433_battery",
			Help: "Battery high (1) or low (0).",
		},
		labels,
	)
)

// Message is a single sensor observation: a single line of JSON input from $ rtl_433 -F json
type Message struct {
	// ISO 8601 Datetime e.g. "2019-05-23 20:41:45"
	Time string `json:"time"`
	// Sensor Model
	Model string `json:"model"`
	// Sensor ID. May be random per-boot, or saved into device memory.
	ID int `json:"id"`
	// Channel sensor is transmitting on. Typically 1-3, controlled by a switch on the device
	// Either an int or string
	Channel interface{} `json:"channel"`
	// Battery status, typically "LOW" or "OK", case-insensitive.
	Battery string `json:"battery"`
	// Temperature in Celsius. Nil if not present in initial JSON.
	Temperature *float64 `json:"temperature_C"`
	// Humidity (0-100). Nil if not present in initial JSON.
	Humidity *int32 `json:"humidity"`
}

func main() {
	flag.Parse()

	prometheus.MustRegister(packetsReceived)
	prometheus.MustRegister(temperature)
	prometheus.MustRegister(humidity)
	prometheus.MustRegister(timestamp)
	prometheus.MustRegister(battery)

	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			index.Execute(w, "")
		})
		http.Handle("/metrics", prometheus.Handler())
		if err := http.ListenAndServe(*addr, nil); err != nil {
			log.Fatal(err)
		}
	}()

	cmd := exec.Command("/bin/bash", "-c", *subprocess)
	// If we don't tell the subprocess stderr to be our stderr, we get no logs on failure.
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		msg := Message{}
		line := scanner.Bytes()
		if err := json.Unmarshal(line, &msg); err != nil {
			log.Fatal(err)
		}

		// Some sensors output numbered channels, some output string channels.
		// We have to handle both.
		var strChannel string
		if s, ok := msg.Channel.(string); ok {
			strChannel = s
		} else if floatChannel, ok := msg.Channel.(float64); ok {
			strChannel = fmt.Sprintf("%f", floatChannel)
		} else {
			log.Fatalf("Could not parse JSON %v, bad channel (expected float or string): %v", line, msg.Channel)
		}

		labels := []string{msg.Model, strconv.Itoa(msg.ID), strChannel}
		packetsReceived.WithLabelValues(labels...).Inc()
		timestamp.WithLabelValues(labels...).SetToCurrentTime()
		if temperature != nil {
			temperature.WithLabelValues(labels...).Set(*msg.Temperature)
		}
		if msg.Humidity != nil {
			humidity.WithLabelValues(labels...).Set(float64(*msg.Humidity) / 100)
		}
		switch {
		case strings.EqualFold(msg.Battery, "OK"):
			battery.WithLabelValues(labels...).Set(1)
		case strings.EqualFold(msg.Battery, "LOW"):
			battery.WithLabelValues(labels...).Set(0)
		}
	}
	// Wait first, then check scanner.Err, because Wait's error messages are better.
	if err := cmd.Wait(); err != nil {
		log.Fatal(err)
	}
	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
}
