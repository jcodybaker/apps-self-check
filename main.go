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
	"github.com/go-sql-driver/mysql"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	defaultPort    uint64 = 8080
	defaultBindArr        = "0.0.0.0"
)

func main() {
	port := defaultPort
	bindAddr := defaultBindArr

	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	ll := zerolog.New(os.Stdout)
	ctx := ll.WithContext(context.Background())

	mysqlLogger := ll.With().Str("component", "mysql").Logger()
	mysql.SetLogger(&mysqlLogger)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
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

	s, err := storer.New(ctx, os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_CA_CERT"), true)
	if err != nil {
		log.Fatal().Err(err).Msg("creating storer")
	}
	defer s.Close()

	instance, err := check.NewInstance()
	if err != nil {
		log.Fatal().Err(err).Msg("initializing instance")
	}

	instance.AppID = os.Getenv("APP_ID")
	instance.Labels, err = parseLabels(os.Getenv("LABELS"))
	if err != nil {
		log.Fatal().Err(err).Msg("parsing labels")
	}

	s.AsyncQueryRetry(
		ctx,
		storer.SaveBackOffSchedule,
		func(ctx context.Context, attempt int) error {
			return s.UpdateInstance(ctx, instance)
		})

	checkerOpts := []checker.CheckerOption{
		checker.WithInstance(instance),
		checker.WithStorer(s),
	}

	if publicURL := os.Getenv("PUBLIC_URL"); publicURL != "" {
		checkerOpts = append(checkerOpts, checker.WithCheck("self_public_http", checkMust(checker.NewHTTPCheck(publicURL))))
	}
	if privateDomain := os.Getenv("PRIVATE_DOMAIN"); privateDomain != "" {
		checkerOpts = append(checkerOpts,
			checker.WithCheck("self_private_url", checkMust(checker.NewHTTPCheck("http://"+privateDomain+":8080/health"))),
			checker.WithCheck("internal_dns", checkMust(checker.NewDNSCheck(privateDomain, "10.0.0.0/8"))),
		)
	}

	if checkDB := os.Getenv("CHECK_DATABASE_URL"); checkDB != "" {
		if strings.HasPrefix(checkDB, "mysql") {
			checkerOpts = append(checkerOpts,
				checker.WithCheck("database", checkMust(checker.NewMySQLCheck(checkDB, os.Getenv("CHECK_DATABASE_CA_CERT")))))
		}
		checkerOpts = append(checkerOpts,
			checker.WithCheck("database_dns", checkMust(checker.NewDNSCheck(checkDB, os.Getenv("CHECK_DATABASE_CIDR")))))
	}

	c := checker.NewChecker(checkerOpts...)
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/check", c.CheckHandler)
	mux.HandleFunc("/errors", c.ErrorsHandler)

	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		instance.Lock()
		instance.StoppedAt = time.Now()
		instance.Unlock()
		s.AsyncQueryRetry(
			context.Background(),
			storer.SaveBackOffSchedule,
			func(ctx context.Context, attempt int) error {
				return s.UpdateInstance(ctx, instance)
			})
	}()
	s.AsyncQueryRetry(
		ctx,
		storer.SaveBackOffSchedule,
		func(ctx context.Context, attempt int) error {
			if err := instance.UpdatePublicIPv4(ctx); err != nil {
				return fmt.Errorf("querying node public IPv4: %w", err)
			}
			return s.UpdateInstance(ctx, instance)
		})

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
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Error().Err(err).Msg("shutting down server")
		}
	}()
	if err := server.ListenAndServe(); err != nil && ctx.Err() == nil {
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
