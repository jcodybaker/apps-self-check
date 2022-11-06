package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/digitalocean/apps-self-check/pkg/checker"
	"github.com/digitalocean/apps-self-check/pkg/storer"
	"github.com/digitalocean/apps-self-check/pkg/types/check"
	"github.com/rs/zerolog/log"
)

const (
	defaultPort    uint64 = 8080
	defaultBindArr        = "0.0.0.0"
)

func main() {
	port := defaultPort
	bindAddr := defaultBindArr

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if p := os.Getenv("PORT"); p != "" {
		var err error
		if port, err = strconv.ParseUint(p, 10, 16); err != nil {
			log.Fatal().Err(err).Msg("failed to parse port")
		}
	}
	if b := os.Getenv("BIND_ADDR"); b != "" {
		bindAddr = b
	}

	storer, err := storer.NewMySQLStorer(ctx, os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_CA_CERT"))
	if err != nil {
		log.Fatal().Err(err).Msg("creating storer")
	}
	defer storer.Close()

	labels, err := parseLabels(os.Getenv("LABELS"))
	if err != nil {
		log.Fatal().Err(err).Msg("parsing labels")
	}

	checkerOpts := []checker.CheckerOption{
		checker.WithAppID(os.Getenv("APP_ID")),
		checker.WithCheck("self_public_http", checkMust(checker.NewHTTPCheck(os.Getenv("PUBLIC_URL")))),
		checker.WithCheck("self_private_url", checkMust(checker.NewHTTPCheck("http://"+os.Getenv("PRIVATE_DOMAIN")+":8080/health"))),
		checker.WithCheck("internal_dns", checkMust(checker.NewDNSCheck(os.Getenv("PRIVATE_DOMAIN")))),
		checker.WithLabels(labels),
		checker.WithStorer(storer),
	}

	if checkDB := os.Getenv("CHECK_DATABASE_URL"); checkDB != "" {
		if strings.HasPrefix(checkDB, "mysql") {
			checkerOpts = append(checkerOpts,
				checker.WithCheck("database", checkMust(checker.NewMySQLCheck(checkDB, os.Getenv("CHECK_DATABASE_CA_CERT")))))
		}
		checkerOpts = append(checkerOpts,
			checker.WithCheck("database_dns", checkMust(checker.NewDNSCheck(checkDB))))
	}

	c := checker.NewChecker(checkerOpts...)
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/check", c.HTTPHandler)

	var wg sync.WaitGroup
	defer wg.Wait()

	if checkIntervalS := os.Getenv("CHECK_INTERVAL"); checkIntervalS != "" {
		checkInterval, err := time.ParseDuration(checkIntervalS)
		if err != nil {
			log.Fatal().Err(err).Msg("parsing CHECK_INTERVAL")
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Run(ctx, checkInterval)
		}()
	}

	serverAddr := net.JoinHostPort(bindAddr, strconv.Itoa(int(port)))
	log.Ctx(ctx).Info().Str("addr", serverAddr).Msg("starting server")
	server := http.Server{
		Addr:    serverAddr,
		Handler: mux,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatal().Err(err).Msg("failed to start server")
	}
}

func checkMust(c check.Check, err error) check.Check {
	if err == nil {
		return c
	}
	log.Fatal().Err(err).Msg("failed to start server")
	return nil // Should never be called.
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	http.Error(w, http.StatusText(http.StatusOK), http.StatusOK)
}

func parseLabels(labels string) (map[string]string, error) {
	out := make(map[string]string)
	asQuery, err := url.ParseQuery(labels)
	if err != nil {
		return nil, fmt.Errorf("parsing labels: %w", err)
	}
	for k, vs := range asQuery {
		if len(vs) == 0 {
			continue
		}
		if len(vs) > 1 {
			return nil, fmt.Errorf("label %q had multiple values", k)
		}
		out[k] = vs[0]
	}
	return out, nil
}
